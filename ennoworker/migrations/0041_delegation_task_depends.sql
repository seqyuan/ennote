-- Task concept unification Stage 1: dynamic task graph dependencies.
-- delegation_items gain a batch-scoped depends declaration. Task names refer
-- to sibling items in the same delegation group; the topology is validated at
-- creation (no dangling refs, no cycles, at least one entry task) and drives
-- readiness scheduling plus blocked propagation for dependent tasks.
ALTER TABLE delegation_items ADD COLUMN depends_json TEXT NOT NULL DEFAULT '[]';
