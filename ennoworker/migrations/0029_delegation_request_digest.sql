-- Item 6 correctness repair: bind idempotency keys to canonical generation
-- request payloads. Historical rows remain replay-compatible when the digest
-- is NULL; every newly created retry or continuation stores a digest.
ALTER TABLE delegation_group_generations ADD COLUMN request_digest TEXT;
