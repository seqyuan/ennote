package globalsource

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/graphsource"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/seqyuan/ennote/ennoworker/internal/skills"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentSourcesUseSharedAgentsRoot(t *testing.T) {
	home := t.TempDir()
	store := Store{HomeDir: home}
	assert.Equal(t, filepath.Join(home, "agents"), store.AgentsDir())
	assert.Equal(t, filepath.Join(home, "agents", "heroes"), store.HeroesDir())
	assert.Equal(t, filepath.Join(home, "agents", "graphs"), store.GraphsDir())
}

func TestGraphRevisionsAreImmutableAndDigestIdempotent(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)

	first, err := store.PublishGraphRevision("rna-seq")
	require.NoError(t, err)
	assert.Equal(t, "v000001", first.ID())
	assert.Equal(t, digest, first.Digest)
	again, err := store.PublishGraphRevision("rna-seq")
	require.NoError(t, err)
	assert.Equal(t, first, again)

	revisionSource := filepath.Join(store.GraphsDir(), "rna-seq", "revisions", "v000001", "graph.yaml")
	original, err := os.ReadFile(revisionSource)
	require.NoError(t, err)
	_, nextDigest, err := store.UpdateGraph("rna-seq", digest, func(document *graphsource.Document) error {
		document.Description = "Updated source"
		return nil
	})
	require.NoError(t, err)
	second, err := store.PublishGraphRevision("rna-seq")
	require.NoError(t, err)
	assert.Equal(t, "v000002", second.ID())
	assert.Equal(t, nextDigest, second.Digest)
	unchanged, err := os.ReadFile(revisionSource)
	require.NoError(t, err)
	assert.Equal(t, original, unchanged)

	revisions, err := store.ListGraphRevisions("rna-seq")
	require.NoError(t, err)
	require.Len(t, revisions, 2)
	assert.Equal(t, []int{1, 2}, []int{revisions[0].Version, revisions[1].Version})
}

func TestHeroRevisionPublishesCanonicalHeroMarkdown(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	hero := &heroesource.Document{
		SchemaVersion: 1, Handle: "reviewer", Name: "Reviewer",
		Model:  heroesource.ModelBinding{Ref: "openai-main/gpt-5", ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills: []heroesource.SkillBinding{}, AllowedTools: []string{"read"},
		Context:    heroesource.ContextPolicy{AllowedModes: []domain.HeroContextMode{}},
		Delegation: heroesource.DelegationPolicy{AllowedCallerKinds: []string{}, AllowedStrategies: []string{}},
		Prompt:     "Review the supplied work.",
	}
	_, digest, err := store.CreateHero(hero)
	require.NoError(t, err)
	revision, err := store.PublishHeroRevision("reviewer")
	require.NoError(t, err)
	assert.Equal(t, digest, revision.Digest)
	contents, err := os.ReadFile(filepath.Join(store.HeroesDir(), "reviewer", "revisions", "v000001", "hero.md"))
	require.NoError(t, err)
	parsed, err := heroesource.Parse(contents)
	require.NoError(t, err)
	assert.Equal(t, hero.Handle, parsed.Handle)
	assert.Equal(t, hero.Prompt, parsed.Prompt)
}

func TestRevisionMetadataRejectsTampering(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, _, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)
	_, err = store.PublishGraphRevision("rna-seq")
	require.NoError(t, err)
	metadata := filepath.Join(store.GraphsDir(), "rna-seq", "revisions", "v000001", "revision.json")
	require.NoError(t, os.WriteFile(metadata, []byte(`{"schemaVersion":1,"resourceId":"other","version":1,"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","publishedAt":"2026-08-10T00:00:00Z"}`), 0o600))

	_, err = store.ListGraphRevisions("rna-seq")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata is invalid")
}

func graphLocalHero(handle string, skillBindings ...string) *heroesource.Document {
	bindings := make([]heroesource.SkillBinding, 0, len(skillBindings))
	for _, id := range skillBindings {
		bindings = append(bindings, heroesource.SkillBinding{ID: id, Mode: domain.HeroSkillPreload})
	}
	return &heroesource.Document{
		SchemaVersion: 1,
		Handle:        handle,
		Name:          handle,
		Model:         heroesource.ModelBinding{Ref: "openai-main/gpt-5", ThinkingEffort: domain.ThinkingDefault, Fallbacks: []string{}},
		Skills:        bindings,
		AllowedTools:  []string{"read"},
		Context:       heroesource.ContextPolicy{AllowedModes: []domain.HeroContextMode{}},
		Delegation:    heroesource.DelegationPolicy{AllowedCallerKinds: []string{}, AllowedStrategies: []string{}},
		Prompt:        "Do the task.",
	}
}

func writeGraphSkill(t *testing.T, store Store, graphID, skillID, body string) string {
	t.Helper()
	dir := filepath.Join(store.GraphsDir(), graphID, "skills", skillID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.json"),
		[]byte(`{"id":"`+skillID+`","version":"0.1.0","prompt":"SKILL.md"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600))
	return dir
}

func setupGraphWithLocalHero(t *testing.T, store Store, graphID string) string {
	t.Helper()
	_, digest, err := store.CreateGraph(graphID, graphID)
	require.NoError(t, err)
	heroDigest, err := store.CreateGraphHero(graphID, graphLocalHero("reviewer"))
	require.NoError(t, err)
	_, _, err = store.UpdateGraph(graphID, digest, func(document *graphsource.Document) error {
		document.Tasks["review"] = graphsource.Task{Name: "Review", Hero: "local/reviewer", Goal: "Review"}
		document.Graph["review"] = []string{}
		return nil
	})
	require.NoError(t, err)
	_, err = store.PublishGraphRevision(graphID)
	require.NoError(t, err)
	return heroDigest
}

func TestPublishGraphFreezesLocalHeroAndSurvivesDraftEdits(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	heroDigest := setupGraphWithLocalHero(t, store, "rna-seq")

	revisions, err := store.ListGraphRevisions("rna-seq")
	require.NoError(t, err)
	require.Len(t, revisions, 1)
	assert.Equal(t, map[string]string{"reviewer": heroDigest}, revisions[0].HeroDigests)
	assert.Empty(t, revisions[0].SkillDigests)

	heroPath := filepath.Join(store.GraphsDir(), "rna-seq", "revisions", "v000001", "heroes", "reviewer", "hero.md")
	contents, err := os.ReadFile(heroPath)
	require.NoError(t, err)
	parsed, err := heroesource.Parse(contents)
	require.NoError(t, err)
	assert.Equal(t, "reviewer", parsed.Handle)
	assert.Equal(t, "Do the task.", parsed.Prompt)

	// Edit the draft local Hero after publish; the frozen revision must not move.
	edited := graphLocalHero("reviewer")
	edited.Prompt = "Edited after publish."
	encoded, err := heroesource.Encode(edited)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(store.GraphsDir(), "rna-seq", "heroes", "reviewer", "hero.md"), encoded, 0o600))

	bundle, err := store.ReadGraphRevisionBundle("rna-seq", "v000001")
	require.NoError(t, err)
	require.True(t, bundle.Frozen)
	require.Contains(t, bundle.Heroes, "reviewer")
	assert.Equal(t, "Do the task.", bundle.Heroes["reviewer"].Prompt)
	assert.Equal(t, heroDigest, bundle.Revision.HeroDigests["reviewer"])
}

func TestReadGraphRevisionBundleRejectsTamperedHero(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	setupGraphWithLocalHero(t, store, "rna-seq")

	heroPath := filepath.Join(store.GraphsDir(), "rna-seq", "revisions", "v000001", "heroes", "reviewer", "hero.md")
	edited := graphLocalHero("reviewer")
	edited.Prompt = "tampered prompt"
	encoded, err := heroesource.Encode(edited)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(heroPath, encoded, 0o600))

	_, err = store.ReadGraphRevisionBundle("rna-seq", "v000001")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed integrity validation")
}

func TestPublishGraphFreezesLocalSkillAndRejectsTamper(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)
	skillDir := writeGraphSkill(t, store, "rna-seq", "review-tools", "Review tools prompt.")
	_, _, err = store.UpdateGraph("rna-seq", digest, func(document *graphsource.Document) error {
		document.Tasks["review"] = graphsource.Task{Name: "Review", Model: "openai-main/gpt-5", Goal: "Review", Skills: []string{"local/review-tools"}}
		document.Graph["review"] = []string{}
		return nil
	})
	require.NoError(t, err)

	revision, err := store.PublishGraphRevision("rna-seq")
	require.NoError(t, err)
	require.Len(t, revision.SkillDigests, 1)
	skillDigest, ok := revision.SkillDigests["review-tools"]
	require.True(t, ok)
	sourceDigest, err := skills.ContentDigest(skillDir)
	require.NoError(t, err)
	assert.Equal(t, sourceDigest, skillDigest)

	bundle, err := store.ReadGraphRevisionBundle("rna-seq", "v000001")
	require.NoError(t, err)
	require.True(t, bundle.Frozen)
	require.Contains(t, bundle.SkillDirs, "review-tools")
	frozenDigest, err := skills.ContentDigest(bundle.SkillDirs["review-tools"])
	require.NoError(t, err)
	assert.Equal(t, skillDigest, frozenDigest)

	require.NoError(t, os.WriteFile(filepath.Join(bundle.SkillDirs["review-tools"], "SKILL.md"), []byte("tampered"), 0o600))
	_, err = store.ReadGraphRevisionBundle("rna-seq", "v000001")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed integrity validation")
}

func TestPublishGraphVersionBumpsWhenOnlySkillChanges(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)
	skillDir := writeGraphSkill(t, store, "rna-seq", "review-tools", "v1")
	_, _, err = store.UpdateGraph("rna-seq", digest, func(document *graphsource.Document) error {
		document.Tasks["review"] = graphsource.Task{Name: "Review", Model: "openai-main/gpt-5", Goal: "Review", Skills: []string{"local/review-tools"}}
		document.Graph["review"] = []string{}
		return nil
	})
	require.NoError(t, err)

	first, err := store.PublishGraphRevision("rna-seq")
	require.NoError(t, err)
	assert.Equal(t, "v000001", first.ID())

	// Change only the local Skill; the Graph document digest stays identical.
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("v2"), 0o600))
	second, err := store.PublishGraphRevision("rna-seq")
	require.NoError(t, err)
	assert.Equal(t, "v000002", second.ID())
	assert.NotEqual(t, first.SkillDigests["review-tools"], second.SkillDigests["review-tools"])

	third, err := store.PublishGraphRevision("rna-seq")
	require.NoError(t, err)
	assert.Equal(t, "v000002", third.ID())
}

func TestReadGraphRevisionBundleLegacyRevisionNotFrozen(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, _, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)
	revision, err := store.PublishGraphRevision("rna-seq")
	require.NoError(t, err)
	assert.Empty(t, revision.HeroDigests)
	assert.Empty(t, revision.SkillDigests)

	bundle, err := store.ReadGraphRevisionBundle("rna-seq", "v000001")
	require.NoError(t, err)
	assert.False(t, bundle.Frozen)
	assert.Empty(t, bundle.Heroes)
	assert.Empty(t, bundle.SkillDirs)
}

func TestPublishGraphRejectsSymlinkedSkill(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)
	skillDir := writeGraphSkill(t, store, "rna-seq", "review-tools", "body")
	require.NoError(t, os.Symlink(filepath.Join(skillDir, "SKILL.md"), filepath.Join(skillDir, "extra.md")))
	_, _, err = store.UpdateGraph("rna-seq", digest, func(document *graphsource.Document) error {
		document.Tasks["review"] = graphsource.Task{Name: "Review", Model: "openai-main/gpt-5", Goal: "Review", Skills: []string{"local/review-tools"}}
		document.Graph["review"] = []string{}
		return nil
	})
	require.NoError(t, err)

	_, err = store.PublishGraphRevision("rna-seq")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
}

func TestPublishGraphRejectsOversizedSkill(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)
	skillDir := writeGraphSkill(t, store, "rna-seq", "review-tools", "body")
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "big.bin"), make([]byte, maxSkillDirBytes+1), 0o600))
	_, _, err = store.UpdateGraph("rna-seq", digest, func(document *graphsource.Document) error {
		document.Tasks["review"] = graphsource.Task{Name: "Review", Model: "openai-main/gpt-5", Goal: "Review", Skills: []string{"local/review-tools"}}
		document.Graph["review"] = []string{}
		return nil
	})
	require.NoError(t, err)

	_, err = store.PublishGraphRevision("rna-seq")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "freeze limit")
}

// A local Hero may reference a graph-local Skill. Publishing must freeze that
// Skill too: the Hero document alone does not carry its text, so a revision that
// froze only the Hero would resolve different content at Run time.
func TestPublishGraphFreezesSkillReferencedByLocalHero(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)
	writeGraphSkill(t, store, "rna-seq", "review-tools", "Review tools prompt.")
	_, err = store.CreateGraphHero("rna-seq", graphLocalHero("reviewer", "local/review-tools"))
	require.NoError(t, err)
	_, _, err = store.UpdateGraph("rna-seq", digest, func(document *graphsource.Document) error {
		document.Tasks["review"] = graphsource.Task{Name: "Review", Hero: "local/reviewer", Goal: "Review"}
		document.Graph["review"] = []string{}
		return nil
	})
	require.NoError(t, err)

	revision, err := store.PublishGraphRevision("rna-seq")
	require.NoError(t, err)
	require.Contains(t, revision.HeroDigests, "reviewer")
	require.Contains(t, revision.SkillDigests, "review-tools")

	bundle, err := store.ReadGraphRevisionBundle("rna-seq", "v000001")
	require.NoError(t, err)
	require.Contains(t, bundle.Heroes, "reviewer")
	require.Contains(t, bundle.SkillDirs, "review-tools")
	contents, err := os.ReadFile(filepath.Join(bundle.SkillDirs["review-tools"], "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, "Review tools prompt.", string(contents))
}

// A Skill directory with no regular files yields a digest but no files to write,
// so publishing it would produce a revision whose skills/<id> directory never
// exists. Reject it at publish instead of failing every later read.
func TestPublishGraphRejectsFilelessSkill(t *testing.T) {
	store := Store{HomeDir: t.TempDir()}
	_, digest, err := store.CreateGraph("rna-seq", "RNA-seq")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(store.GraphsDir(), "rna-seq", "skills", "empty-tools"), 0o700))
	_, _, err = store.UpdateGraph("rna-seq", digest, func(document *graphsource.Document) error {
		document.Tasks["review"] = graphsource.Task{Name: "Review", Model: "openai-main/gpt-5", Goal: "Review", Skills: []string{"local/empty-tools"}}
		document.Graph["review"] = []string{}
		return nil
	})
	require.NoError(t, err)

	_, err = store.PublishGraphRevision("rna-seq")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains no files")
}
