-- Addressable Role identities and immutable published versions.
-- Existing agent profiles remain Host execution profiles and are not exposed as Roles.

ALTER TABLE agent_profiles ADD COLUMN object_kind TEXT NOT NULL DEFAULT 'host_profile';
ALTER TABLE agent_profiles ADD COLUMN handle TEXT;
ALTER TABLE agent_profiles ADD COLUMN scope TEXT NOT NULL DEFAULT 'global';
ALTER TABLE agent_profiles ADD COLUMN project_id TEXT REFERENCES projects(id);
ALTER TABLE agent_profiles ADD COLUMN icon TEXT NOT NULL DEFAULT 'bot';
ALTER TABLE agent_profiles ADD COLUMN color TEXT NOT NULL DEFAULT 'neutral';
ALTER TABLE agent_profiles ADD COLUMN positioning TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_profiles ADD COLUMN draft_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE agent_profiles ADD COLUMN draft_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_profiles ADD COLUMN delegation_enabled INTEGER NOT NULL DEFAULT 1;
ALTER TABLE agent_profiles ADD COLUMN delegation_revocation_epoch INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agent_profiles ADD COLUMN delegation_disabled_at TEXT;

CREATE TABLE agent_profile_versions (
    id               TEXT PRIMARY KEY,
    agent_profile_id TEXT NOT NULL REFERENCES agent_profiles(id) ON DELETE CASCADE,
    version          INTEGER NOT NULL,
    definition_json  TEXT NOT NULL,
    config_digest    TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'published',
    created_at       TEXT NOT NULL,
    UNIQUE(agent_profile_id, version)
);

ALTER TABLE agent_profiles ADD COLUMN current_version_id TEXT REFERENCES agent_profile_versions(id);

ALTER TABLE room_member_instances ADD COLUMN role_version_id TEXT REFERENCES agent_profile_versions(id);

CREATE UNIQUE INDEX ux_room_members_role_version
ON room_member_instances(session_id, role_id, role_version_id)
WHERE role_version_id IS NOT NULL;

CREATE TRIGGER room_member_role_version_validate_insert
BEFORE INSERT ON room_member_instances
WHEN NEW.role_version_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM agent_profile_versions v
    WHERE v.id=NEW.role_version_id AND v.agent_profile_id=NEW.role_id
)
BEGIN
    SELECT RAISE(ABORT, 'room_member_role_version_invalid');
END;

CREATE UNIQUE INDEX ux_roles_global_handle
ON agent_profiles(handle)
WHERE object_kind='role' AND project_id IS NULL AND status='active';

CREATE UNIQUE INDEX ux_roles_project_handle
ON agent_profiles(project_id, handle)
WHERE object_kind='role' AND project_id IS NOT NULL AND status='active';

CREATE TRIGGER agent_profiles_role_validate_insert
BEFORE INSERT ON agent_profiles
WHEN NEW.object_kind NOT IN ('host_profile', 'role')
  OR json_valid(NEW.draft_json)=0
  OR NEW.draft_revision < 0
  OR NEW.delegation_enabled NOT IN (0, 1)
  OR NEW.delegation_revocation_epoch < 0
  OR (NEW.object_kind='role' AND (
      NEW.handle IS NULL OR length(NEW.handle) < 2 OR length(NEW.handle) > 32
      OR NEW.handle NOT GLOB '[a-z]*' OR NEW.handle GLOB '*[^a-z0-9_-]*'
      OR NEW.scope NOT IN ('builtin', 'global', 'project')
      OR (NEW.scope='project' AND NEW.project_id IS NULL)
      OR (NEW.scope IN ('builtin', 'global') AND NEW.project_id IS NOT NULL)
  ))
BEGIN
    SELECT RAISE(ABORT, 'role_identity_invalid');
END;

CREATE TRIGGER agent_profiles_role_validate_update
BEFORE UPDATE OF object_kind,handle,scope,project_id,draft_json,draft_revision,delegation_enabled,delegation_revocation_epoch
ON agent_profiles
WHEN NEW.object_kind NOT IN ('host_profile', 'role')
  OR json_valid(NEW.draft_json)=0
  OR NEW.draft_revision < 0
  OR NEW.delegation_enabled NOT IN (0, 1)
  OR NEW.delegation_revocation_epoch < 0
  OR (NEW.object_kind='role' AND (
      NEW.handle IS NULL OR length(NEW.handle) < 2 OR length(NEW.handle) > 32
      OR NEW.handle NOT GLOB '[a-z]*' OR NEW.handle GLOB '*[^a-z0-9_-]*'
      OR NEW.scope NOT IN ('builtin', 'global', 'project')
      OR (NEW.scope='project' AND NEW.project_id IS NULL)
      OR (NEW.scope IN ('builtin', 'global') AND NEW.project_id IS NOT NULL)
  ))
BEGIN
    SELECT RAISE(ABORT, 'role_identity_invalid');
END;

CREATE TRIGGER agent_profiles_object_kind_immutable
BEFORE UPDATE OF object_kind ON agent_profiles
WHEN NEW.object_kind <> OLD.object_kind
BEGIN
    SELECT RAISE(ABORT, 'agent_profile_object_kind_immutable');
END;

CREATE TRIGGER agent_profile_versions_validate_insert
BEFORE INSERT ON agent_profile_versions
WHEN NEW.version < 1
  OR NEW.status <> 'published'
  OR json_valid(NEW.definition_json)=0
  OR NEW.config_digest NOT GLOB 'sha256:[0-9a-f]*'
  OR length(NEW.config_digest) <> 71
  OR NOT EXISTS (
      SELECT 1 FROM agent_profiles p
      WHERE p.id=NEW.agent_profile_id AND p.object_kind='role'
  )
BEGIN
    SELECT RAISE(ABORT, 'role_version_invalid');
END;

CREATE TRIGGER agent_profile_versions_immutable_update
BEFORE UPDATE ON agent_profile_versions
BEGIN
    SELECT RAISE(ABORT, 'role_version_immutable');
END;

CREATE TRIGGER agent_profile_versions_immutable_delete
BEFORE DELETE ON agent_profile_versions
BEGIN
    SELECT RAISE(ABORT, 'role_version_immutable');
END;

CREATE TRIGGER agent_profiles_current_version_validate
BEFORE UPDATE OF current_version_id ON agent_profiles
WHEN NEW.current_version_id IS NOT NULL
 AND NOT EXISTS (
     SELECT 1 FROM agent_profile_versions v
     WHERE v.id=NEW.current_version_id AND v.agent_profile_id=NEW.id
 )
BEGIN
    SELECT RAISE(ABORT, 'role_current_version_invalid');
END;

INSERT INTO settings(key,value)
VALUES('hosted_commit_format_version','1');
