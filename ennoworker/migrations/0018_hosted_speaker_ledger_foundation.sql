-- Hosted speaker ledger compatibility foundation.
-- This migration is additive: legacy message graph IDs and parent pointers are unchanged.

ALTER TABLE sessions ADD COLUMN mode TEXT NOT NULL DEFAULT 'hosted';

CREATE TRIGGER sessions_mode_validate_insert
BEFORE INSERT ON sessions
WHEN NEW.mode NOT IN ('hosted', 'room')
BEGIN
    SELECT RAISE(ABORT, 'session_mode_invalid');
END;

CREATE TRIGGER sessions_mode_validate_update
BEFORE UPDATE OF mode ON sessions
WHEN NEW.mode NOT IN ('hosted', 'room')
BEGIN
    SELECT RAISE(ABORT, 'session_mode_invalid');
END;

CREATE TRIGGER sessions_mode_immutable
BEFORE UPDATE OF mode ON sessions
WHEN NEW.mode <> OLD.mode
BEGIN
    SELECT RAISE(ABORT, 'session_mode_immutable');
END;

CREATE TABLE room_member_instances (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    role_id TEXT NOT NULL REFERENCES agent_profiles(id),
    created_at TEXT NOT NULL,
    UNIQUE(session_id, id)
);

ALTER TABLE messages ADD COLUMN speaker_kind TEXT NOT NULL DEFAULT 'host';
ALTER TABLE messages ADD COLUMN speaker_object_id TEXT;
ALTER TABLE messages ADD COLUMN speaker_version_id TEXT;
ALTER TABLE messages ADD COLUMN participant_instance_id TEXT REFERENCES room_member_instances(id);
ALTER TABLE messages ADD COLUMN speaker_snapshot_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE messages ADD COLUMN addressee_kind TEXT;
ALTER TABLE messages ADD COLUMN addressee_object_id TEXT;
ALTER TABLE messages ADD COLUMN addressee_version_id TEXT;
ALTER TABLE messages ADD COLUMN reply_to_message_id TEXT REFERENCES messages(id);
ALTER TABLE messages ADD COLUMN visibility TEXT NOT NULL DEFAULT 'public';
ALTER TABLE messages ADD COLUMN originated_at TEXT;

CREATE TRIGGER messages_attribution_validate_insert
BEFORE INSERT ON messages
WHEN NEW.speaker_kind NOT IN ('user', 'host', 'role', 'workflow', 'room', 'system')
  OR NEW.visibility NOT IN ('public', 'private', 'room_control', 'legacy_execution')
  OR (NEW.addressee_kind IS NOT NULL AND NEW.addressee_kind NOT IN ('host', 'room', 'role', 'graph', 'workflow'))
BEGIN
    SELECT RAISE(ABORT, 'message_attribution_invalid');
END;

CREATE TRIGGER messages_attribution_validate_update
BEFORE UPDATE OF speaker_kind, visibility, addressee_kind ON messages
WHEN NEW.speaker_kind NOT IN ('user', 'host', 'role', 'workflow', 'room', 'system')
  OR NEW.visibility NOT IN ('public', 'private', 'room_control', 'legacy_execution')
  OR (NEW.addressee_kind IS NOT NULL AND NEW.addressee_kind NOT IN ('host', 'room', 'role', 'graph', 'workflow'))
BEGIN
    SELECT RAISE(ABORT, 'message_attribution_invalid');
END;

UPDATE messages
SET speaker_kind = 'user',
    speaker_snapshot_json = '{"kind":"user","displayName":"You"}',
    addressee_kind = 'host',
    visibility = 'public',
    originated_at = created_at
WHERE role = 'user' AND run_id IS NULL;

UPDATE messages
SET speaker_kind = 'host',
    speaker_snapshot_json = '{"kind":"host","displayName":"Host"}',
    visibility = 'legacy_execution',
    originated_at = created_at
WHERE run_id IS NOT NULL;

UPDATE messages
SET visibility = 'public'
WHERE id IN (
    SELECT assistant_message_id FROM agent_runs WHERE assistant_message_id IS NOT NULL
);

UPDATE messages
SET speaker_kind = 'host',
    speaker_snapshot_json = '{"kind":"host","displayName":"Host"}',
    visibility = 'public',
    originated_at = created_at
WHERE role = 'assistant' AND run_id IS NULL;

ALTER TABLE turns ADD COLUMN input_message_id TEXT REFERENCES messages(id);
ALTER TABLE turns ADD COLUMN input_kind TEXT NOT NULL DEFAULT 'user_message';
ALTER TABLE turns ADD COLUMN target_kind TEXT NOT NULL DEFAULT 'host';
ALTER TABLE turns ADD COLUMN target_object_id TEXT;
ALTER TABLE turns ADD COLUMN target_version_id TEXT;
ALTER TABLE turns ADD COLUMN target_participant_instance_id TEXT REFERENCES room_member_instances(id);
ALTER TABLE turns ADD COLUMN context_mode TEXT NOT NULL DEFAULT 'room';
ALTER TABLE turns ADD COLUMN reply_to_json TEXT NOT NULL DEFAULT '[]';

UPDATE turns SET input_message_id = user_message_id;

CREATE TRIGGER turns_hosted_contract_validate_insert
BEFORE INSERT ON turns
WHEN NEW.input_kind NOT IN ('user_message', 'room_control')
  OR NEW.target_kind NOT IN ('host', 'room', 'role', 'graph', 'workflow')
  OR NEW.context_mode NOT IN ('room', 'fresh', 'reply_to')
BEGIN
    SELECT RAISE(ABORT, 'turn_hosted_contract_invalid');
END;

CREATE TRIGGER turns_hosted_contract_validate_update
BEFORE UPDATE OF input_kind, target_kind, context_mode ON turns
WHEN NEW.input_kind NOT IN ('user_message', 'room_control')
  OR NEW.target_kind NOT IN ('host', 'room', 'role', 'graph', 'workflow')
  OR NEW.context_mode NOT IN ('room', 'fresh', 'reply_to')
BEGIN
    SELECT RAISE(ABORT, 'turn_hosted_contract_invalid');
END;

ALTER TABLE agent_runs ADD COLUMN speaker_snapshot_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE agent_runs ADD COLUMN context_snapshot_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE agent_runs ADD COLUMN context_snapshot_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_runs ADD COLUMN parent_run_id TEXT REFERENCES agent_runs(id);
ALTER TABLE agent_runs ADD COLUMN root_run_id TEXT REFERENCES agent_runs(id);
ALTER TABLE agent_runs ADD COLUMN execution_depth INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_runs ADD COLUMN publish_mode TEXT NOT NULL DEFAULT 'public_final';
ALTER TABLE agent_runs ADD COLUMN commit_format_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE agent_runs ADD COLUMN system_prompt_snapshot_json TEXT NOT NULL DEFAULT '{}';

UPDATE agent_runs
SET speaker_snapshot_json = '{"kind":"host","displayName":"Host"}',
    root_run_id = id,
    commit_format_version = 1;

CREATE TRIGGER agent_runs_commit_format_validate_insert
BEFORE INSERT ON agent_runs
WHEN NEW.commit_format_version NOT IN (1, 2)
BEGIN
    SELECT RAISE(ABORT, 'run_commit_format_invalid');
END;

CREATE TRIGGER agent_runs_commit_format_validate_update
BEFORE UPDATE OF commit_format_version ON agent_runs
WHEN NEW.commit_format_version NOT IN (1, 2)
BEGIN
    SELECT RAISE(ABORT, 'run_commit_format_invalid');
END;

CREATE TRIGGER agent_runs_commit_format_immutable
BEFORE UPDATE OF commit_format_version ON agent_runs
WHEN NEW.commit_format_version <> OLD.commit_format_version
BEGIN
    SELECT RAISE(ABORT, 'run_commit_format_immutable');
END;

CREATE TRIGGER agent_runs_publish_mode_validate_insert
BEFORE INSERT ON agent_runs
WHEN NEW.publish_mode NOT IN ('public_final', 'private_to_parent')
BEGIN
    SELECT RAISE(ABORT, 'run_publish_mode_invalid');
END;

ALTER TABLE model_profiles ADD COLUMN thinking_dialect TEXT NOT NULL DEFAULT 'none';
ALTER TABLE model_profiles ADD COLUMN supported_thinking_efforts_json TEXT NOT NULL DEFAULT '["default"]';

CREATE TABLE run_messages (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    role TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    visibility TEXT NOT NULL DEFAULT 'private',
    created_at TEXT NOT NULL,
    UNIQUE(run_id, ordinal)
);

CREATE INDEX ix_run_messages_run_ordinal ON run_messages(run_id, ordinal);

CREATE TRIGGER run_messages_validate_insert
BEFORE INSERT ON run_messages
WHEN NEW.ordinal < 0
  OR NEW.role NOT IN ('system', 'user', 'assistant', 'tool')
  OR NEW.visibility NOT IN ('private', 'public')
  OR json_valid(NEW.payload_json) = 0
BEGIN
    SELECT RAISE(ABORT, 'run_message_invalid');
END;
