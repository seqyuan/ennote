#!/bin/sh
# RunStart hook: inject current git branch and workspace identity into the
# model's first turn as background context.
#
# Install:
#   {
#     "hooks": {
#       "RunStart": {
#         "matchers": [
#           { "id": "inject-git-branch", "hooks": [
#             { "id": "inject-branch-command", "type": "command", "command": "/path/to/inject-git-branch.sh" }
#           ]}
#         ]
#       }
#     }
#   }
#
# Output: exit 0 + stdout JSON {"additionalContext": "..."}.

branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "no-git")
workspace=$(basename "$ENNOTE_WORKSPACE_ROOT" 2>/dev/null || echo "unknown")

# JSON-escape the context text for the additionalContext field.
escaped_branch=$(printf '%s' "$branch" | sed 's/\\/\\\\/g; s/"/\\"/g')
escaped_ws=$(printf '%s' "$workspace" | sed 's/\\/\\\\/g; s/"/\\"/g')

printf '{"additionalContext": "Working workspace: %s. Current git branch: %s. Use this context for file operations and commit hygiene."}\n' \
  "$escaped_ws" "$escaped_branch"

exit 0
