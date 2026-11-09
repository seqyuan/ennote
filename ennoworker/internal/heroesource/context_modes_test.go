package heroesource_test

import (
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/heroesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseAcceptsDelegationAndReplyContextModes pins that the portable Hero
// document carries the full frozen HeroContextPolicy vocabulary. A Hero that
// declares task_only can run as a delegated child; reply enables reply_to
// addressing. Neither was authorable before, so UI-created Heroes could never be
// delegated to.
func TestParseAcceptsDelegationAndReplyContextModes(t *testing.T) {
	input := strings.Replace(validHeroMarkdown,
		"  defaultMode: room\n  allowedModes: [fresh, room]",
		"  defaultMode: task_only\n  allowedModes: [room, reply, fresh, task_only]", 1)
	document, err := heroesource.Parse([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, domain.HeroContextTask, document.Context.DefaultMode)
	assert.Equal(t, []domain.HeroContextMode{
		domain.HeroContextFresh, domain.HeroContextReply, domain.HeroContextRoom, domain.HeroContextTask,
	}, document.Context.AllowedModes)

	encoded, err := heroesource.Encode(document)
	require.NoError(t, err)
	roundTripped, err := heroesource.Parse(encoded)
	require.NoError(t, err)
	assert.Equal(t, document, roundTripped)
}

func TestParseRejectsUnknownContextMode(t *testing.T) {
	defaultMode := strings.Replace(validHeroMarkdown, "defaultMode: room", "defaultMode: teleport", 1)
	_, err := heroesource.Parse([]byte(defaultMode))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported context defaultMode")

	allowedMode := strings.Replace(validHeroMarkdown, "allowedModes: [fresh, room]", "allowedModes: [fresh, teleport]", 1)
	_, err = heroesource.Parse([]byte(allowedMode))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported context allowedModes")
}
