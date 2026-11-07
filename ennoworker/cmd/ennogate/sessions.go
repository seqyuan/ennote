package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Login sessions outlive the process.
//
// A session is stored as HMAC(password hash, token) next to its expiry, and
// validated by recomputing the digest. Three properties fall out of that choice:
//
//   - a restart keeps its sessions. The seven-day cookie always promised that and
//     the in-memory map never delivered it: every restart of the gate logged out
//     every browser, which is also why the e2e harness had to run against one long
//     lived process.
//   - the file never holds a usable bearer token. What is on disk is a keyed hash,
//     so a copy of it cannot be replayed even though a copy of the token could.
//   - changing the password changes the key, so every session issued under the old
//     one stops validating at once. The file records which key it was written
//     under, so the old set is dropped rather than left to expire in place.
//
// The digest set is kept in memory and written through on every change, because
// validation runs on every request: a file read per request would put the disk in
// the path of every SSE frame's neighbours.
const sessionTTL = 7 * 24 * time.Hour

// sessionKeyDomain keeps the HMAC key from being confused with anything else
// derived from the password hash.
const sessionKeyDomain = "ennote-gate-session-v1"

type gateSessions struct {
	mu     sync.Mutex
	path   string
	digest map[string]time.Time // token digest -> expiry
	// epoch is the fingerprint of the key these digests belong to, and the value
	// `bind` compares to decide whether the set is still worth keeping.
	key   []byte
	epoch string // fingerprint of the key the loaded sessions belong to
}

type sessionFile struct {
	// Epoch is the fingerprint of the HMAC key these sessions were issued under.
	// A session digest issued under a different password is not merely unmatched:
	// it is a session nobody can ever validate again, so it is dropped.
	Epoch    string            `json:"epoch"`
	Sessions map[string]string `json:"sessions"` // digest -> RFC 3339 expiry
}

// sessionPath is per port, mirroring the cookie name: two gates on one home keep
// separate session sets, and neither can clobber the other's file.
func sessionPath(home, port string) string {
	return filepath.Join(home, "config", fmt.Sprintf("sessions-%s.json", port))
}

func newGateSessions(home, port string) *gateSessions {
	return &gateSessions{path: sessionPath(home, port), digest: map[string]time.Time{}}
}

// bind points the store at a password hash, loading the sessions issued under it.
// It is called on every authenticated request and is a no-op unless the hash has
// changed, so a password change takes effect without a restart.
//
// Persistence is a convenience, not a dependency: this file is what lets a restart
// keep you logged in, and the gate has to keep working when it cannot be read or
// written — a read-only home directory must not turn into "no one can log in".
// Everything that fails here is logged and the store carries on in memory, which is
// exactly the behaviour before sessions were persisted.
//
// The new state is committed only after the read succeeds. Assigning the epoch
// first would leave a failed load looking like a completed one, and the next
// bind would short-circuit on the epoch it never actually loaded.
func (s *gateSessions) bind(passwordHash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sessionKey(passwordHash)
	epoch := sessionEpoch(key)
	if s.key != nil && epoch == s.epoch {
		return
	}

	digest := map[string]time.Time{}
	stored := sessionFile{}
	contents, err := os.ReadFile(s.path)
	switch {
	case err == nil:
		if decodeErr := json.Unmarshal(contents, &stored); decodeErr != nil {
			// A corrupt file is not a reason to refuse every login: start clean.
			stored = sessionFile{}
		}
	case !errors.Is(err, os.ErrNotExist):
		slog.Error("gate could not read the session file; sessions will not survive a restart",
			"path", s.path, "error", err)
		stored = sessionFile{}
	}

	if stored.Epoch == epoch {
		now := time.Now()
		for digestValue, expiry := range stored.Sessions {
			expiresAt, parseErr := time.Parse(time.RFC3339, expiry)
			if parseErr != nil || !now.Before(expiresAt) {
				continue
			}
			digest[digestValue] = expiresAt
		}
	}

	s.key = key
	s.epoch = epoch
	s.digest = digest
	// Rewrite either way: a stale epoch and expired entries both leave the file
	// saying something the store no longer believes.
	s.writeThroughLocked("bind")
}

// count reports how many sessions are live. It exists because the map is only
// reachable under the mutex: an unlocked len() is a concurrent-map read.
func (s *gateSessions) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.digest)
}

// issue records a token as a live session. The caller already decided the player
// could log in.
func (s *gateSessions) issue(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == nil {
		return fmt.Errorf("gate sessions are not bound to a password yet")
	}
	s.digest[s.sum(token)] = time.Now().Add(sessionTTL)
	s.writeThroughLocked("issue")
	return nil
}

// valid reports whether the token is a live session, dropping it if it expired.
func (s *gateSessions) valid(token string) bool {
	if !isSessionToken(token) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == nil {
		return false
	}
	sum := s.sum(token)
	expiresAt, ok := s.digest[sum]
	if !ok {
		return false
	}
	if !time.Now().Before(expiresAt) {
		// Expiry comes from the wall clock, not from the file, so a failed write
		// here cannot resurrect the session: the next bind prunes it again.
		delete(s.digest, sum)
		s.writeThroughLocked("expiry")
		return false
	}
	return true
}

// isSessionToken keeps HMAC off attacker-sized input: the cookie is caller-supplied
// and bounded only by the header limit.
func isSessionToken(token string) bool {
	return len(token) == 64 && strings.Trim(token, "0123456789abcdef") == ""
}

// revoke ends one session. Logging out has to mean the token stops working, not
// just that the browser forgets it.
//
// A write that fails leaves the revocation in memory only: this process rejects the
// token, and a later restart would not. That is logged as an error rather than
// failing the logout, because the caller's intent — end this browser's session —
// has already been carried out here.
func (s *gateSessions) revoke(token string) error {
	if !isSessionToken(token) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	digest := s.sum(token)
	if _, ok := s.digest[digest]; !ok {
		return nil
	}
	delete(s.digest, digest)
	s.writeThroughLocked("revoke")
	return nil
}

func (s *gateSessions) sum(token string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

// writeThroughLocked persists the current set and reports a failure without
// failing the operation it belongs to: the in-memory set is the authority for this
// process, the file only has to agree when the next process starts.
func (s *gateSessions) writeThroughLocked(reason string) {
	if err := s.writeLocked(); err != nil {
		slog.Error("gate could not persist its sessions; they will not survive a restart",
			"path", s.path, "reason", reason, "error", err)
	}
}

func (s *gateSessions) writeLocked() error {
	if s.key == nil {
		return nil
	}
	encoded := sessionFile{Epoch: s.epoch, Sessions: map[string]string{}}
	for digest, expiry := range s.digest {
		encoded.Sessions[digest] = expiry.UTC().Format(time.RFC3339)
	}
	contents, err := json.MarshalIndent(encoded, "", "  ")
	if err != nil {
		return fmt.Errorf("encode gate sessions: %w", err)
	}
	contents = append(contents, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".sessions-*")
	if err != nil {
		return fmt.Errorf("create session file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect session file: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return fmt.Errorf("write session file: %w", err)
	}
	// The promise is that a restart keeps its sessions; a crash is a restart too.
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("flush session file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close session file: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace session file: %w", err)
	}
	return nil
}

func sessionKey(passwordHash string) []byte {
	mac := hmac.New(sha256.New, []byte(sessionKeyDomain))
	mac.Write([]byte(passwordHash))
	return mac.Sum(nil)
}

// sessionEpoch names the key, so the file can tell "issued under this password"
// from "issued under a previous one" without storing the key or the hash.
func sessionEpoch(key []byte) string {
	sum := sha256.Sum256(append([]byte(sessionKeyDomain+":epoch"), key...))
	return hex.EncodeToString(sum[:16])
}

func (g *Gate) cookieName() string {
	return fmt.Sprintf("ennote_session_%s", g.Port)
}

// sessionStore is created on first use, so a Gate built in a test needs no wiring.
func (g *Gate) sessionStore() *gateSessions {
	g.sessionsOnce.Do(func() { g.sessions = newGateSessions(g.HomeDir, g.Port) })
	return g.sessions
}

// authHash reads the stored password hash. configured reports whether a password
// exists at all: the gate answers a setup request differently in that state.
func (g *Gate) authHash() (hash string, configured bool, err error) {
	contents, err := os.ReadFile(filepath.Join(g.HomeDir, "config", "auth.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var stored struct{ Hash string }
	if err := json.Unmarshal(contents, &stored); err != nil {
		return "", true, fmt.Errorf("decode auth file: %w", err)
	}
	return stored.Hash, true, nil
}

// boundSessions returns the session set that belongs to this password. Binding is
// a no-op unless the password changed, so this is safe on every request and is how
// a password change takes effect without a restart.
func (g *Gate) boundSessions(passwordHash string) *gateSessions {
	sessions := g.sessionStore()
	sessions.bind(passwordHash)
	return sessions
}

func (g *Gate) setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: g.cookieName(), Value: token, Path: "/",
		HttpOnly: true, Secure: g.SecureCookie, SameSite: http.SameSiteLaxMode,
		MaxAge: maxAge,
	})
}

func generateToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate secure token: %w", err)
	}
	return hex.EncodeToString(value), nil
}
