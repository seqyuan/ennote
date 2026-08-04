-- Item 6 Stage 2: ordered optional auto-resume.
-- Auto-resume delivers a background completion as one automatic follow-up Host
-- run on the source session branch, in completion sequence order, while no
-- top-level run is active. The continuation Run is an ordinary agent Run with
-- a system-originated input turn; uniqueness is enforced by the source
-- completion id, so at most one continuation Run is ever created per
-- completion. This migration only widens the turns.input_kind CHECK; the
-- input_kind CHECK is baked into the table, so SQLite requires a full table
-- rebuild. Migration 26 runs with foreign_keys OFF (see store.Migrate) with a
-- foreign_key_check afterwards.

CREATE TABLE turns_v26 (
    id                           TEXT PRIMARY KEY,
    session_id                   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    client_request_id            TEXT NOT NULL,
    user_message_id              TEXT NOT NULL REFERENCES messages(id),
    base_message_id              TEXT,
    status                       TEXT NOT NULL DEFAULT 'pending',
    created_at                   TEXT NOT NULL,
    updated_at                   TEXT NOT NULL,
    input_message_id             TEXT REFERENCES messages(id),
    input_kind                   TEXT NOT NULL DEFAULT 'user_message'
                                     CHECK(input_kind IN ('user_message','room_control','delegation_completion')),
    target_kind                  TEXT NOT NULL DEFAULT 'host',
    target_object_id             TEXT,
    target_version_id            TEXT,
    target_participant_instance_id TEXT REFERENCES room_member_instances(id),
    context_mode                 TEXT NOT NULL DEFAULT 'room',
    reply_to_json                TEXT NOT NULL DEFAULT '[]'
);

INSERT INTO turns_v26
    (id,session_id,client_request_id,user_message_id,base_message_id,status,created_at,updated_at,
     input_message_id,input_kind,target_kind,target_object_id,target_version_id,target_participant_instance_id,
     context_mode,reply_to_json)
SELECT id,session_id,client_request_id,user_message_id,base_message_id,status,created_at,updated_at,
       input_message_id,input_kind,target_kind,target_object_id,target_version_id,target_participant_instance_id,
       context_mode,reply_to_json
FROM turns;

DROP TABLE turns;
ALTER TABLE turns_v26 RENAME TO turns;

CREATE UNIQUE INDEX IF NOT EXISTS ux_turns_client_request
    ON turns(session_id, client_request_id);
