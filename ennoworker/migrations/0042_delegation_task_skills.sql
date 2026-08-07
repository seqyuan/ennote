-- Task concept unification review fix: freeze task-level Skill IDs on the
-- delegation item. These IDs are additive execution preloads; Role tool policy
-- and authority remain the capability boundary. The item-level snapshot keeps
-- retries and restart recovery on the same task contract.
ALTER TABLE delegation_items ADD COLUMN skills_json TEXT NOT NULL DEFAULT '[]';
