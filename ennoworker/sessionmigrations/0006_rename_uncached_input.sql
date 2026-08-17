-- model_calls.input_tokens now stores the uncached-input bucket (see 0005).
-- Rename the column so the schema says what it means. No data rewrite is
-- needed: this project has not shipped, so there is no legacy total-input data
-- to reconcile.
ALTER TABLE model_calls RENAME COLUMN input_tokens TO uncached_input_tokens;
