package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hexToken builds a token of the shape generateToken produces (32 random bytes as
// hex), so the store's shape check is exercised the way production hits it.
func hexToken(pair string) string { return strings.Repeat(pair, 32) }

func testSessions(t *testing.T, passwordHash string) *gateSessions {
	t.Helper()
	sessions := newGateSessions(t.TempDir(), "45131")
	sessions.bind(passwordHash)
	return sessions
}

func TestGateSessionsSurviveARestart(t *testing.T) {
	home := t.TempDir()
	before := newGateSessions(home, "45131")
	before.bind("password-hash")
	require.NoError(t, before.issue(hexToken("ab")))

	// A new gate process over the same home: same port, same password.
	after := newGateSessions(home, "45131")
	after.bind("password-hash")

	assert.True(t, after.valid(hexToken("ab")), "a session has to outlive the process that issued it")
	assert.False(t, after.valid(hexToken("cd")))
}

func TestGateSessionsKeepTheTokenOffDisk(t *testing.T) {
	sessions := testSessions(t, "password-hash")
	require.NoError(t, sessions.issue(hexToken("ef")))

	contents, err := os.ReadFile(sessions.path)
	require.NoError(t, err)
	assert.NotContains(t, string(contents), hexToken("ef"),
		"the file must hold a keyed digest, not a bearer token")

	info, err := os.Stat(sessions.path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestGateSessionsEndWhenThePasswordChanges(t *testing.T) {
	home := t.TempDir()
	before := newGateSessions(home, "45131")
	before.bind("password-hash")
	require.NoError(t, before.issue(hexToken("ab")))
	assert.True(t, before.valid(hexToken("ab")))

	// Resetting a forgotten password means deleting auth.json and setting a new
	// one. The old password's sessions must not survive that.
	after := newGateSessions(home, "45131")
	after.bind("a-different-password-hash")

	assert.False(t, after.valid(hexToken("ab")))

	// The stale set is dropped from the file too, not left to expire in place.
	contents, err := os.ReadFile(after.path)
	require.NoError(t, err)
	var stored sessionFile
	require.NoError(t, json.Unmarshal(contents, &stored))
	assert.Empty(t, stored.Sessions)
}

func TestGateSessionsDropExpiredEntries(t *testing.T) {
	sessions := testSessions(t, "password-hash")
	require.NoError(t, sessions.issue(hexToken("ab")))

	// Rewrite the stored expiry into the past rather than waiting a week.
	contents, err := os.ReadFile(sessions.path)
	require.NoError(t, err)
	var stored sessionFile
	require.NoError(t, json.Unmarshal(contents, &stored))
	for digest := range stored.Sessions {
		stored.Sessions[digest] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	}
	encoded, err := json.Marshal(stored)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sessions.path, encoded, 0o600))

	reloaded := newGateSessions(filepath.Dir(filepath.Dir(sessions.path)), "45131")
	reloaded.bind("password-hash")
	assert.False(t, reloaded.valid(hexToken("ab")))

	after, err := os.ReadFile(sessions.path)
	require.NoError(t, err)
	var pruned sessionFile
	require.NoError(t, json.Unmarshal(after, &pruned))
	assert.Empty(t, pruned.Sessions, "an expired session is not carried forward")
}

func TestGateSessionsRevokeOnLogout(t *testing.T) {
	sessions := testSessions(t, "password-hash")
	require.NoError(t, sessions.issue(hexToken("ab")))
	require.True(t, sessions.valid(hexToken("ab")))

	require.NoError(t, sessions.revoke(hexToken("ab")))

	assert.False(t, sessions.valid(hexToken("ab")), "logging out has to end the session, not just the cookie")

	// The revocation is durable: a copy of the token is dead after a restart too.
	reloaded := newGateSessions(filepath.Dir(filepath.Dir(sessions.path)), "45131")
	reloaded.bind("password-hash")
	assert.False(t, reloaded.valid(hexToken("ab")))
}

func TestGateSessionsAreScopedToTheirPort(t *testing.T) {
	home := t.TempDir()
	first := newGateSessions(home, "45131")
	first.bind("password-hash")
	require.NoError(t, first.issue(hexToken("ab")))

	// A second gate on another port shares the home but not the cookie name, so it
	// must not share the session set — or the file.
	second := newGateSessions(home, "45132")
	second.bind("password-hash")
	assert.False(t, second.valid(hexToken("ab")))
	assert.True(t, first.valid(hexToken("ab")))

	assert.FileExists(t, sessionPath(home, "45131"))
	assert.FileExists(t, sessionPath(home, "45132"))
}

func TestGateSessionsRefuseToIssueBeforeAPasswordExists(t *testing.T) {
	sessions := newGateSessions(t.TempDir(), "45131")

	// Before bind there is no key, so there is nothing to key a digest with.
	assert.Error(t, sessions.issue(hexToken("ab")))
	assert.False(t, sessions.valid(hexToken("ab")))
	assert.False(t, sessions.valid(""), "an absent cookie is never a session")
}

func TestGateSessionsRebindIsCheapAndIdempotent(t *testing.T) {
	sessions := testSessions(t, "password-hash")
	require.NoError(t, sessions.issue(hexToken("ab")))

	// Every authenticated request binds; doing so must not drop the session.
	sessions.bind("password-hash")
	sessions.bind("password-hash")

	assert.True(t, sessions.valid(hexToken("ab")))
}

func TestGateAuthHashReadsTheFileAndReportsWhetherAPasswordExists(t *testing.T) {
	home := t.TempDir()
	gate := &Gate{HomeDir: home}

	hash, configured, err := gate.authHash()
	require.NoError(t, err)
	assert.False(t, configured)
	assert.Empty(t, hash)

	require.NoError(t, os.MkdirAll(filepath.Join(home, "config"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, "config", "auth.json"), []byte(`{"hash":"bcrypt-hash"}`), 0o600))

	hash, configured, err = gate.authHash()
	require.NoError(t, err)
	assert.True(t, configured)
	assert.Equal(t, "bcrypt-hash", hash)
}

func TestGateSessionFileRejectsUnreadableContents(t *testing.T) {
	sessions := newGateSessions(t.TempDir(), "45131")
	require.NoError(t, os.MkdirAll(filepath.Dir(sessions.path), 0o700))
	require.NoError(t, os.WriteFile(sessions.path, []byte("{not json"), 0o600))

	// A corrupt file must not lock the operator out: start clean and say so by
	// rewriting it, rather than refusing every login with no way back.
	sessions.bind("password-hash")
	require.NoError(t, sessions.issue(hexToken("ab")))
	assert.True(t, sessions.valid(hexToken("ab")))

	contents, err := os.ReadFile(sessions.path)
	require.NoError(t, err)
	var rewritten sessionFile
	require.NoError(t, json.Unmarshal(contents, &rewritten), "the file has to be rewritten as valid JSON")
	assert.Len(t, rewritten.Sessions, 1)
}

func TestGateSessionsRebindToANewPasswordEndsTheSession(t *testing.T) {
	// The same instance, a new password: this is the case the epoch exists for.
	// Before sessions were keyed by the password, `ennote passwd` rewrote
	// auth.json and every session kept working.
	sessions := testSessions(t, "password-hash")
	require.NoError(t, sessions.issue(hexToken("ab")))
	require.True(t, sessions.valid(hexToken("ab")))

	sessions.bind("a-new-password-hash")

	assert.False(t, sessions.valid(hexToken("ab")),
		"a password change has to end the sessions issued under the old one")
	assert.Equal(t, 0, sessions.count())
}

func TestGateSessionsExpireOnTheirOwn(t *testing.T) {
	// Expiry is decided by the clock on every check, not only when the file is
	// loaded: a gate left running for a week must not keep a session alive.
	sessions := testSessions(t, "password-hash")
	require.NoError(t, sessions.issue(hexToken("ab")))

	sessions.mu.Lock()
	for digest := range sessions.digest {
		sessions.digest[digest] = time.Now().Add(-time.Second)
	}
	sessions.mu.Unlock()

	assert.False(t, sessions.valid(hexToken("ab")))
	assert.Equal(t, 0, sessions.count(), "an expired session is dropped when it is noticed")
}

func TestGateSessionsKeepWorkingWhenTheFileCannotBeWritten(t *testing.T) {
	// Persistence is a convenience. A read-only home directory must cost you a
	// restart's worth of logins, not the ability to log in at all.
	home := t.TempDir()
	blocked := filepath.Join(home, "config")
	require.NoError(t, os.MkdirAll(blocked, 0o700))
	require.NoError(t, os.Chmod(blocked, 0o500))
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })

	sessions := newGateSessions(home, "45131")
	sessions.bind("password-hash")
	require.NoError(t, sessions.issue(hexToken("ab")))

	assert.True(t, sessions.valid(hexToken("ab")), "the session still works for this process")
	_, err := os.Stat(sessions.path)
	assert.True(t, errors.Is(err, os.ErrNotExist), "and nothing was persisted")
}

func TestGateSessionEpochIsStable(t *testing.T) {
	// The epoch is what a future build compares against the file it finds. Changing
	// the derivation without knowing it logs out every browser on upgrade, so pin
	// the value: a deliberate change has to update this expectation and say so.
	assert.Equal(t, "8f5184eee8a523f3a8774541b4510f3e", sessionEpoch(sessionKey("password-hash")))
}

func TestGateSessionsIgnoreATokenThatIsNotOneOfTheirs(t *testing.T) {
	// The cookie is caller-supplied; the shape check keeps the HMAC off
	// attacker-sized input and rejects anything that could not be a token.
	sessions := testSessions(t, "password-hash")
	require.NoError(t, sessions.issue(hexToken("ab")))

	assert.False(t, sessions.valid(""))
	assert.False(t, sessions.valid("token-1"))
	assert.False(t, sessions.valid(strings.Repeat("z", 64)))
	assert.False(t, sessions.valid(strings.Repeat("ab", 64)))
}

func TestGateSessionsAreSafeUnderConcurrentUse(t *testing.T) {
	// Every map access goes through the mutex, and CI runs the suite with -race:
	// this is the test that keeps it that way. An unlocked read of the set — a
	// count for a log line, an inspection in a handler — is a fatal error in Go,
	// not a wrong answer.
	sessions := testSessions(t, "password-hash")

	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			token := hexToken(strconv.Itoa(worker) + "a")
			for attempt := 0; attempt < 25; attempt++ {
				_ = sessions.issue(token)
				_ = sessions.valid(token)
				_ = sessions.revoke(token)
				_ = sessions.count()
			}
		}(worker)
	}
	group.Wait()

	assert.Equal(t, 0, sessions.count(), "every worker revoked what it issued")
}
