package rolesource_test

import (
	"strings"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/domain"
	"github.com/seqyuan/ennote/ennoworker/internal/rolesource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseAcceptsDelegationAndReplyContextModes pins that the portable Role
// document carries the full frozen RoleContextPolicy vocabulary. A Role that
// declares task_only can run as a delegated child; reply enables reply_to
// addressing. Neither was authorable before, so UI-created Roles could never be
// delegated to.
func TestParseAcceptsDelegationAndReplyContextModes(t *testing.T) {
	input := strings.Replace(validRoleMarkdown,
		"  defaultMode: room\n  allowedModes: [fresh, room]",
		"  defaultMode: task_only\n  allowedModes: [room, reply, fresh, task_only]", 1)
	document, err := rolesource.Parse([]byte(input))
	require.NoError(t, err)
	assert.Equal(t, domain.RoleContextTask, document.Context.DefaultMode)
	assert.Equal(t, []domain.RoleContextMode{
		domain.RoleContextFresh, domain.RoleContextReply, domain.RoleContextRoom, domain.RoleContextTask,
	}, document.Context.AllowedModes)

	encoded, err := rolesource.Encode(document)
	require.NoError(t, err)
	roundTripped, err := rolesource.Parse(encoded)
	require.NoError(t, err)
	assert.Equal(t, document, roundTripped)
}

func TestParseRejectsUnknownContextMode(t *testing.T) {
	defaultMode := strings.Replace(validRoleMarkdown, "defaultMode: room", "defaultMode: teleport", 1)
	_, err := rolesource.Parse([]byte(defaultMode))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported context defaultMode")

	allowedMode := strings.Replace(validRoleMarkdown, "allowedModes: [fresh, room]", "allowedModes: [fresh, teleport]", 1)
	_, err = rolesource.Parse([]byte(allowedMode))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported context allowedModes")
}
