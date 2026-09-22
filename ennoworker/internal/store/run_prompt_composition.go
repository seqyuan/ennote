package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/systemprompt"
)

// ErrSystemPromptCompositionFrozen reports an attempt to record a different
// system prompt composition for a Run that already recorded one. The
// composition is part of the Run's frozen identity, so this is a programming
// error, never a user error, and it must fail closed.
var ErrSystemPromptCompositionFrozen = errors.New("system prompt composition is already frozen")

// RunPromptComposition is the frozen model-visible input of one Run: the exact
// composed prompt text, the ordered identity of every contribution, and the
// digests that bind them to the Run's frozen base-prompt snapshot.
//
// Prompt is empty when the Run predates composition freezing. Callers must
// render that as "not recorded" rather than as an empty prompt.
type RunPromptComposition struct {
	RunID           string
	Version         int
	AgentProfileID  string
	PlatformVersion string
	// Digest is the frozen base-prompt digest. It covers the Agent prompt and
	// its platform identity, independent of the composition record.
	Digest         string
	Sections       []domain.PromptSection
	SectionsDigest string
	ComposedDigest string
	Prompt         string
}

// LoadRunPromptComposition reads one Run's frozen prompt composition. It is the
// read side of FreezeSystemPromptComposition and the only supported way for a
// carrier to learn what the model was given.
func (r *RunRepo) LoadRunPromptComposition(ctx context.Context, runID string) (RunPromptComposition, error) {
	if runID == "" {
		return RunPromptComposition{}, fmt.Errorf("run id is required")
	}
	var stored string
	if err := r.DB.QueryRowContext(ctx,
		`SELECT system_prompt_snapshot_json FROM agent_runs WHERE id=?`, runID).Scan(&stored); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunPromptComposition{}, ErrRunNotFound
		}
		return RunPromptComposition{}, err
	}
	composition := RunPromptComposition{RunID: runID}
	if stored == "" || stored == "{}" {
		return composition, nil
	}
	var snapshot domain.SystemPromptSnapshot
	if err := json.Unmarshal([]byte(stored), &snapshot); err != nil {
		return RunPromptComposition{}, fmt.Errorf("decode frozen system prompt snapshot: %w", err)
	}
	composition.Version = snapshot.Version
	composition.AgentProfileID = snapshot.AgentProfileID
	composition.PlatformVersion = snapshot.PlatformVersion
	composition.Digest = snapshot.Digest
	composition.Sections = snapshot.Sections
	composition.SectionsDigest = snapshot.SectionsDigest
	composition.ComposedDigest = snapshot.ComposedDigest
	if snapshot.ComposedDigest == "" {
		return composition, nil
	}
	if err := r.DB.QueryRowContext(ctx,
		`SELECT text FROM prompt_blobs WHERE digest=?`, snapshot.ComposedDigest).Scan(&composition.Prompt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The Run recorded a composition whose bytes are gone. Report the
			// record without text rather than inventing or truncating a prompt.
			return composition, nil
		}
		return RunPromptComposition{}, err
	}
	return composition, nil
}

// FreezeSystemPromptComposition records the composition of a Run's system
// prompt: the ordered section identity of every contribution plus the exact
// composed text, addressed by content digest so identical prompts are stored
// once and run rows stay small.
//
// This is the same kind of freeze as the effective config: it happens exactly
// once, while the Run is running, and it is guarded by the Run's own frozen
// snapshot. Repeating it with an identical composition is a no-op so a retried
// start path is safe; a different composition fails with
// ErrSystemPromptCompositionFrozen rather than silently changing what the model
// was given.
func (r *RunRepo) FreezeSystemPromptComposition(ctx context.Context, runID, composed string, sections []domain.PromptSection) error {
	if runID == "" {
		return fmt.Errorf("run id is required")
	}
	sectionsDigest, err := systemprompt.SectionsDigest(sections)
	if err != nil {
		return err
	}
	composedDigest := ""
	if composed != "" {
		composedDigest = systemprompt.TextDigest(composed)
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin prompt composition transaction: %w", err)
	}
	defer tx.Rollback()

	var status domain.RunStatus
	var stored string
	if err := tx.QueryRowContext(ctx,
		`SELECT status, system_prompt_snapshot_json FROM agent_runs WHERE id=?`, runID).Scan(&status, &stored); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRunNotFound
		}
		return err
	}
	if status != domain.RunRunning {
		return fmt.Errorf("%w: prompt composition requires running run", ErrInvalidRunState)
	}

	var snapshot domain.SystemPromptSnapshot
	if stored != "" && stored != "{}" {
		if err := json.Unmarshal([]byte(stored), &snapshot); err != nil {
			return fmt.Errorf("decode frozen system prompt snapshot: %w", err)
		}
		if err := validatePromptSections(snapshot); err != nil {
			return err
		}
	}
	// The composition is recorded against the Run's frozen base prompt, so a Run
	// with no frozen snapshot must not acquire a composition record: there would
	// be nothing binding it to the identity the Run actually validates.
	if snapshot.Digest == "" {
		return fmt.Errorf("%w: prompt composition requires a frozen system prompt snapshot", ErrInvalidRunState)
	}
	if snapshot.SectionsDigest != "" {
		if snapshot.SectionsDigest == sectionsDigest && snapshot.ComposedDigest == composedDigest {
			return tx.Commit()
		}
		return ErrSystemPromptCompositionFrozen
	}

	if composedDigest != "" {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO prompt_blobs (digest, bytes, text, created_at)
			VALUES (?, ?, ?, ?)`, composedDigest, len(composed), composed, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("store composed system prompt: %w", err)
		}
	}
	snapshot.Sections = sections
	snapshot.SectionsDigest = sectionsDigest
	snapshot.ComposedDigest = composedDigest
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode frozen system prompt snapshot: %w", err)
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE agent_runs SET system_prompt_snapshot_json=? WHERE id=? AND status='running'
		 AND system_prompt_digest=?`, string(encoded), runID, snapshot.Digest)
	if err != nil {
		return fmt.Errorf("record prompt composition: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return fmt.Errorf("%w: frozen system prompt snapshot changed concurrently", ErrInvalidRunState)
	}
	return tx.Commit()
}
