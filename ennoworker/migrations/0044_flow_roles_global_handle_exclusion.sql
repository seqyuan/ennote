-- Fix flow-scoped Role handle collisions (design 2026-08-07-flow-scoped-roles):
-- ux_roles_global_handle from 0019 matched ANY role with project_id IS NULL,
-- which also caught scope='flow' roles (their project_id is inherited at
-- resolve time, so it stays NULL). That made a flow-local Role unable to share
-- a handle with a global Role, breaking the documented resolution precedence
-- (bare handle inside the owning flow resolves flow > global).
DROP INDEX ux_roles_global_handle;
CREATE UNIQUE INDEX ux_roles_global_handle
ON agent_profiles(handle)
WHERE object_kind='role' AND project_id IS NULL AND scope!='flow' AND status='active';
