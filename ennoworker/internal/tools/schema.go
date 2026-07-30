package tools

import "encoding/json"

func schema(value string) json.RawMessage { return json.RawMessage(value) }

const pathSchema = `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`
