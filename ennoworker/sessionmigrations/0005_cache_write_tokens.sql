-- model_calls.input_tokens is now the uncached-input bucket (was total prompt
-- input); cache_read_tokens stays the cache-hit bucket, and this adds the
-- cache-write bucket so billed input = input_tokens + cache_read_tokens +
-- cache_write_tokens across every provider.
ALTER TABLE model_calls ADD COLUMN cache_write_tokens INTEGER NOT NULL DEFAULT 0;
