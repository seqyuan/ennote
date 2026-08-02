#!/bin/sh
# PostToolUse hook: run gofmt on written Go files and append the result as
# feedback to the tool result.
#
# Install:
#   {
#     "hooks": {
#       "PostToolUse": {
#         "matchers": [
#           { "id": "format-go", "matcher": "write|edit", "hooks": [
#             { "id": "gofmt-after-write", "type": "command", "command": "/path/to/gofmt.sh", "timeoutSeconds": 30 }
#           ]}
#         ]
#       }
#     }
#   }

payload=$(cat)
path=$(printf '%s' "$payload" | sed -n 's/.*"path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
[ -z "$path" ] && exit 0

case "$path" in
  *.go)
    if [ -f "$path" ]; then
      if gofmt -l "$path" | grep -q .; then
        gofmt -w "$path" 2>/dev/null
        printf '{"additionalContext": "gofmt: formatted %s"}\n' "$path"
      fi
    fi
    ;;
esac
exit 0
