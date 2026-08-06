-- 0034: Agent Flow profile drafts (authoring transport).
--
-- Managed flow profiles keep a validated draft (definition JSON + revision)
-- so the editor can iterate draft -> validation error list -> publish without
-- publishing intermediate states. The immutable versions table is never
-- rewritten; publishing snapshots the draft as the next version.

ALTER TABLE agent_flow_profiles ADD COLUMN draft_json TEXT;
ALTER TABLE agent_flow_profiles ADD COLUMN draft_yaml TEXT;
ALTER TABLE agent_flow_profiles ADD COLUMN draft_revision INTEGER NOT NULL DEFAULT 0;
