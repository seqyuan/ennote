-- Structured questions reuse the approval checkpoint: the same table, the same
-- Run status, and the same CAS/Enqueue path. `kind` distinguishes a tool
-- approval from a question; answers are stored separately from approval items.
--
-- `status='approved'` for a question means "answered". This is deliberate:
-- rebuilding tool_approval_requests to add an `answered` CHECK value would
-- rewrite a core table for a label. Readers must branch on `kind`.
ALTER TABLE tool_approval_requests ADD COLUMN kind TEXT NOT NULL DEFAULT 'tool_approval';
ALTER TABLE tool_approval_requests ADD COLUMN questions_json TEXT;
ALTER TABLE tool_approval_requests ADD COLUMN answers_json TEXT;
