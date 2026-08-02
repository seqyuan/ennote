#!/bin/sh
# PreToolUse hook: block destructive shell commands.
#
# Install by adding to $ENNOTE_HOME/config.json (or a trusted workspace's
# .ennote/config.json):
#
#   {
#     "hooks": {
#       "PreToolUse": {
#         "matchers": [
#           {
#             "id": "block-destructive-bash",
#             "matcher": "bash|exec",
#             "hooks": [
#               { "id": "block-rm-rf-command", "type": "command", "command": "/path/to/block-rm-rf.sh" }
#             ]
#           }
#         ]
#       }
#     }
#   }
#
# Protocol: exit 0 = allow, exit 2 = block (stderr as reason). The payload is
# on stdin as single-line JSON.

payload=$(cat)
cmd=$(printf '%s' "$payload" | sed -n 's/.*"command"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
[ -z "$cmd" ] && exit 0

# Reject rm -rf / and similar destructive forms.
if printf '%s' "$cmd" | grep -Eq 'rm[[:space:]]+(-[a-zA-Z]*r[a-zA-Z]*[[:space:]]+)*-?[a-zA-Z]*f'; then
  echo "blocked: destructive 'rm -rf' style command is not allowed by project policy" >&2
  exit 2
fi
if printf '%s' "$cmd" | grep -Eq '^[[:space:]]*(mkfs|dd)[[:space:]]'; then
  echo "blocked: block-device destructive commands (mkfs/dd) are not allowed" >&2
  exit 2
fi

exit 0
