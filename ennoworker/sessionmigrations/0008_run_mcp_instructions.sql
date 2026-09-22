-- The MCP handshake declares the revision the server agreed to and the usage
-- guidance it wants callers to follow. The frozen Run snapshot recorded neither:
-- the negotiated protocol was written as a literal default, and the instructions
-- were discarded. Both are now observed facts, so a Run's record of what a
-- server asked for is verifiable instead of assumed.
ALTER TABLE run_mcp_servers ADD COLUMN instructions TEXT NOT NULL DEFAULT '';
ALTER TABLE run_mcp_servers ADD COLUMN instructions_digest TEXT NOT NULL DEFAULT '';
