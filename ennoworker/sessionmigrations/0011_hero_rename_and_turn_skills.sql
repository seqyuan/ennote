-- The executor concept was renamed from role to hero after 0001 shipped. The
-- consolidated initial schema is history and is never edited: a database that
-- already applied 0001 keeps its checksum, so it still opens with the released
-- build and upgrades here instead of failing closed on a changed migration.
--
-- Two things move:
--   * the columns that carried the old word, via RENAME COLUMN (SQLite rewrites
--     index and trigger references for us);
--   * the attribution triggers, which enumerate the speaker/addressee kinds and
--     therefore have to be recreated with `hero`. They live in triggers rather
--     than table CHECKs precisely so this stays a cheap migration.
--
-- Message rows keep their own `role` column (system / user / assistant / tool):
-- that is the chat wire vocabulary, not the executor.
ALTER TABLE room_member_instances RENAME COLUMN role_id TO hero_id;
ALTER TABLE room_member_instances RENAME COLUMN role_version_id TO hero_version_id;
ALTER TABLE delegation_items RENAME COLUMN role_version_id TO hero_version_id;
ALTER TABLE delegation_items RENAME COLUMN role_meta_json TO hero_meta_json;
ALTER TABLE run_agent_flow_nodes RENAME COLUMN role_version_id TO hero_version_id;
ALTER TABLE run_agent_flow_nodes RENAME COLUMN role_definition_json TO hero_definition_json;

-- The unique index name is part of the schema a reader greps for; RENAME COLUMN
-- kept its definition, so drop and recreate it under the new word.
DROP INDEX IF EXISTS ux_room_members_role_version;
CREATE UNIQUE INDEX ux_room_members_hero_version
ON room_member_instances(session_id, hero_id, hero_version_id)
WHERE hero_version_id IS NOT NULL;

DROP TRIGGER IF EXISTS messages_attribution_validate_insert;
CREATE TRIGGER messages_attribution_validate_insert
BEFORE INSERT ON messages
WHEN NEW.speaker_kind NOT IN ('user', 'host', 'hero', 'workflow', 'room', 'system')
  OR NEW.visibility NOT IN ('public', 'private', 'room_control', 'legacy_execution')
  OR (NEW.addressee_kind IS NOT NULL AND NEW.addressee_kind NOT IN ('host', 'room', 'hero', 'graph', 'workflow'))
BEGIN
    SELECT RAISE(ABORT, 'message_attribution_invalid');
END;

DROP TRIGGER IF EXISTS messages_attribution_validate_update;
CREATE TRIGGER messages_attribution_validate_update
BEFORE UPDATE OF speaker_kind, visibility, addressee_kind ON messages
WHEN NEW.speaker_kind NOT IN ('user', 'host', 'hero', 'workflow', 'room', 'system')
  OR NEW.visibility NOT IN ('public', 'private', 'room_control', 'legacy_execution')
  OR (NEW.addressee_kind IS NOT NULL AND NEW.addressee_kind NOT IN ('host', 'room', 'hero', 'graph', 'workflow'))
BEGIN
    SELECT RAISE(ABORT, 'message_attribution_invalid');
END;

-- A turn may carry the global Skills the user attached to it. The selection is
-- written once with the turn and read by the config freeze, so it belongs on the
-- same immutable row as the invocation target.
ALTER TABLE turns ADD COLUMN skills_json TEXT NOT NULL DEFAULT '[]';
