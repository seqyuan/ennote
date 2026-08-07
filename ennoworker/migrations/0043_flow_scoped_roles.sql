-- Flow-scoped Roles (design 2026-08-07-flow-scoped-roles-and-sidebar-navigation):
-- agent_profiles gains a flow_id ownership column. scope='flow' roles belong
-- to exactly one Agent Flow profile and are only resolvable from task
-- references inside that flow; they never appear to delegate_tasks or other
-- projects. project_id is inherited from the owning flow's project_scope.
ALTER TABLE agent_profiles ADD COLUMN flow_id TEXT REFERENCES agent_flow_profiles(id);

-- Flow-local handle uniqueness: one handle per flow.
CREATE UNIQUE INDEX ux_roles_flow_handle
ON agent_profiles(flow_id, handle)
WHERE object_kind='role' AND scope='flow' AND status='active';

-- Extend scope CHECK to include 'flow' and enforce ownership consistency.
DROP TRIGGER agent_profiles_role_validate_insert;
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
      OR NEW.scope NOT IN ('builtin', 'global', 'project', 'flow')
      OR (NEW.scope='project' AND NEW.project_id IS NULL)
      OR (NEW.scope IN ('builtin', 'global') AND NEW.project_id IS NOT NULL)
      OR (NEW.scope='flow' AND NEW.flow_id IS NULL)
      OR (NEW.scope IN ('builtin', 'global', 'project') AND NEW.flow_id IS NOT NULL)
  ))
BEGIN
    SELECT RAISE(ABORT, 'role_identity_invalid');
END;

DROP TRIGGER agent_profiles_role_validate_update;
CREATE TRIGGER agent_profiles_role_validate_update
BEFORE UPDATE OF object_kind,handle,scope,project_id,flow_id,draft_json,draft_revision,delegation_enabled,delegation_revocation_epoch
ON agent_profiles
WHEN NEW.object_kind NOT IN ('host_profile', 'role')
  OR json_valid(NEW.draft_json)=0
  OR NEW.draft_revision < 0
  OR NEW.delegation_enabled NOT IN (0, 1)
  OR NEW.delegation_revocation_epoch < 0
  OR (NEW.object_kind='role' AND (
      NEW.handle IS NULL OR length(NEW.handle) < 2 OR length(NEW.handle) > 32
      OR NEW.handle NOT GLOB '[a-z]*' OR NEW.handle GLOB '*[^a-z0-9_-]*'
      OR NEW.scope NOT IN ('builtin', 'global', 'project', 'flow')
      OR (NEW.scope='project' AND NEW.project_id IS NULL)
      OR (NEW.scope IN ('builtin', 'global') AND NEW.project_id IS NOT NULL)
      OR (NEW.scope='flow' AND NEW.flow_id IS NULL)
      OR (NEW.scope IN ('builtin', 'global', 'project') AND NEW.flow_id IS NOT NULL)
  ))
BEGIN
    SELECT RAISE(ABORT, 'role_identity_invalid');
END;
