package domain_test

import (
	"encoding/json"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The frozen Skill payload is written by new runs in the versioned form and must
// still decode the legacy bare ID array so pre-pinning Flow Runs and retries keep
// loading after an upgrade.
func TestDecodeFrozenSkillsAcceptsEveryWrittenShape(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want domain.FrozenSkillRefs
	}{
		{
			name: "versioned digest pinned",
			raw:  `{"schemaVersion":2,"skills":[{"id":"go-dev","digest":"sha256:aaa"},{"id":"review","digest":"sha256:bbb"}]}`,
			want: domain.FrozenSkillRefs{{ID: "go-dev", Digest: "sha256:aaa"}, {ID: "review", Digest: "sha256:bbb"}},
		},
		{
			name: "legacy bare ids are unpinned",
			raw:  `["go-dev","review"]`,
			want: domain.FrozenSkillRefs{{ID: "go-dev"}, {ID: "review"}},
		},
		{
			name: "legacy empty array",
			raw:  `[]`,
			want: domain.FrozenSkillRefs{},
		},
		{
			name: "null",
			raw:  `null`,
			want: domain.FrozenSkillRefs{},
		},
		{
			name: "mixed array",
			raw:  `["go-dev",{"id":"review","digest":"sha256:bbb"}]`,
			want: domain.FrozenSkillRefs{{ID: "go-dev"}, {ID: "review", Digest: "sha256:bbb"}},
		},
		{
			name: "duplicates dedupe keeping first occurrence",
			raw:  `{"schemaVersion":2,"skills":[{"id":"go-dev","digest":"sha256:aaa"},{"id":"go-dev","digest":"sha256:zzz"}]}`,
			want: domain.FrozenSkillRefs{{ID: "go-dev", Digest: "sha256:aaa"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refs, err := domain.DecodeFrozenSkills([]byte(tc.raw))
			require.NoError(t, err)
			assert.Equal(t, tc.want, refs)
		})
	}
}

func TestDecodeFrozenSkillsFailsClosedOnUnknownPayload(t *testing.T) {
	_, err := domain.DecodeFrozenSkills([]byte(`{"schemaVersion":99,"skills":[]}`))
	require.ErrorContains(t, err, "newer than the supported version")

	_, err = domain.DecodeFrozenSkills([]byte(`"go-dev"`))
	require.ErrorContains(t, err, "unrecognised payload")

	_, err = domain.DecodeFrozenSkills([]byte(`[{"digest":"sha256:aaa"}]`))
	require.ErrorContains(t, err, "task skill id is required")
}

func TestEncodeFrozenSkillsWritesTheVersionedForm(t *testing.T) {
	encoded, err := domain.EncodeFrozenSkills(domain.FrozenSkillRefs{
		{ID: " go-dev ", Digest: "sha256:aaa"},
		{ID: "go-dev", Digest: "sha256:duplicate"},
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"schemaVersion":2,"skills":[{"id":"go-dev","digest":"sha256:aaa"}]}`, string(encoded))

	// Round trip: the envelope is stable and re-encodable byte for byte.
	refs, err := domain.DecodeFrozenSkills(encoded)
	require.NoError(t, err)
	reencoded, err := domain.EncodeFrozenSkills(refs)
	require.NoError(t, err)
	assert.Equal(t, string(encoded), string(reencoded))

	// Empty input still writes a valid versioned payload with an empty list.
	empty, err := domain.EncodeFrozenSkills(nil)
	require.NoError(t, err)
	assert.JSONEq(t, `{"schemaVersion":2,"skills":[]}`, string(empty))
	var probe map[string]any
	require.NoError(t, json.Unmarshal(empty, &probe))
	assert.Equal(t, float64(2), probe["schemaVersion"])
}

func TestFrozenSkillRefsExposeIDsAndDigests(t *testing.T) {
	refs := domain.FrozenSkillRefs{{ID: "a", Digest: "sha256:a"}, {ID: "b"}}
	assert.Equal(t, []string{"a", "b"}, refs.IDs())
	digest, ok := refs.Digest("a")
	assert.True(t, ok)
	assert.Equal(t, "sha256:a", digest)
	_, ok = refs.Digest("missing")
	assert.False(t, ok)
	assert.True(t, refs[0].Pinned())
	assert.False(t, refs[1].Pinned())

	ids, err := domain.SkillIDsFromJSON([]byte(`{"schemaVersion":2,"skills":[{"id":"a","digest":"sha256:a"}]}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, ids)
	ids, err = domain.SkillIDsFromJSON([]byte(`["a","b"]`))
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, ids)
}
