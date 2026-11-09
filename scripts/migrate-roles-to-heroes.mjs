#!/usr/bin/env node
/**
 * Rename the pre-GA `role` vocabulary to `hero` in an existing Ennote install.
 *
 * This is a one-shot migration, not a compatibility layer: the Worker no longer
 * reads any `role` name, so an install created before the rename has to be moved
 * rather than tolerated. Two things move:
 *
 *   1. `<ENNOTE_HOME>/agents/roles/` -> `agents/heroes/`, and every `role.md`
 *      inside it -> `hero.md`.
 *   2. Keys inside the JSON blobs SQLite stores: a Run's requested/effective
 *      config (`target.kind`, `heroId`, `heroVersionId`, `heroHandle`, ...) and a
 *      message's `speakerKind` / `speaker_snapshot_json`. The `messages.role` and
 *      `run_messages.role` columns are the chat-message role and are left alone.
 *
 * Usage:
 *   node scripts/migrate-roles-to-heroes.mjs                # dry run
 *   node scripts/migrate-roles-to-heroes.mjs --apply        # write
 *   node scripts/migrate-roles-to-heroes.mjs --apply --home /path/to/.ennote
 *
 * The script is idempotent: a second `--apply` finds nothing to do. It never
 * deletes a role file it cannot read and never rewrites a blob it cannot parse.
 */
import { existsSync, readFileSync, readdirSync, renameSync, statSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";

const args = process.argv.slice(2);
const apply = args.includes("--apply");
const homeFlag = args.indexOf("--home");
const home = homeFlag >= 0 ? args[homeFlag + 1] : join(process.env.ENNOTE_HOME ?? join(homedir(), ".ennote"));

const log = (...parts) => console.log(apply ? "apply:" : "dry-run:", ...parts);

/** Executor-side keys that still carry the old word. */
const KEY_ALIASES = [
  ["roleId", "heroId"],
  ["roleVersionId", "heroVersionId"],
  ["roleObjectId", "heroObjectId"],
  ["roleHandle", "heroHandle"],
  ["roleDisplayName", "heroDisplayName"],
  ["rolePrompt", "heroPrompt"],
  ["roleMeta", "heroMeta"],
  ["role", "hero"],
];

/** Message-role values keep their name; everything else `"role"` is an executor. */
function renameKeys(value, seen = 0) {
  if (seen > 40 || value === null || typeof value !== "object") return value;
  if (Array.isArray(value)) return value.map((item) => renameKeys(item, seen + 1));
  const out = {};
  for (const [key, entry] of Object.entries(value)) {
    let nextKey = key;
    for (const [oldKey, newKey] of KEY_ALIASES) {
      if (key === oldKey) { nextKey = newKey; break; }
    }
    out[nextKey] = renameKeys(entry, seen + 1);
  }
  return out;
}

/** Rewrite one JSON blob into the hero vocabulary. */
function renameBlob(json) {
  let parsed;
  try {
    parsed = JSON.parse(json);
  } catch {
    return null;
  }
  const next = renameKeys(parsed);
  if (next?.speakerKind === "role") next.speakerKind = "hero";
  // `kind: "role"` is the executor attribution in a speaker snapshot, a Run
  // target and a delegation meta blob alike. The message's own role lives in a
  // column, not here, so this flip is unconditional.
  if (next?.kind === "role") next.kind = "hero";
  return JSON.stringify(next);
}

function walk(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) walk(path);
    else if (entry.isFile() && entry.name === "role.md") {
      const target = join(dir, "hero.md");
      log("rename file", path, "->", target);
      if (apply) renameSync(path, target);
    }
  }
}

const rolesDir = join(home, "agents", "roles");
const heroesDir = join(home, "agents", "heroes");
if (existsSync(rolesDir)) {
  if (existsSync(heroesDir)) {
    log("SKIP directory: both agents/roles and agents/heroes exist — merge by hand", rolesDir);
  } else {
    log("rename directory", rolesDir, "->", heroesDir);
    if (apply) renameSync(rolesDir, heroesDir);
  }
} else {
  log("no agents/roles directory (already migrated or never created)");
}
if (existsSync(heroesDir)) walk(heroesDir);

/** Every SQLite file an install can hold: the shared ones and one per Session. */
function databasePaths(root) {
  const found = [];
  const look = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const path = join(dir, entry.name);
      if (entry.isDirectory()) look(path);
      else if (entry.isFile() && entry.name.endsWith(".db")) found.push(path);
    }
  };
  look(root);
  return found;
}

const databaseFiles = databasePaths(home);
if (databaseFiles.length === 0) {
  log("no SQLite files; nothing to rewrite");
  process.exit(0);
}
for (const path of databaseFiles) {
  const name = path.slice(home.length + 1);
  if (!statSync(path).isFile()) continue;
  let db;
  try {
    db = new DatabaseSync(path);
  } catch (error) {
    log("SKIP database (cannot open):", path, String(error));
    continue;
  }
  const tables = db.prepare("SELECT name FROM sqlite_master WHERE type='table'").all().map((row) => row.name);
  // `isMessage` only protects the message's own `role` column value; a speaker
  // snapshot's `kind` is the executor attribution and must move with the rest.
  const rewrites = [
    ["agent_runs", "requested_config_json", false],
    ["agent_runs", "effective_config_json", false],
    ["messages", "speaker_snapshot_json", true],
    ["delegation_items", "role_meta_json", false],
    ["delegation_items", "hero_meta_json", false],
    ["run_agent_flow_nodes", "role_definition_json", false],
    ["run_agent_flow_nodes", "hero_definition_json", false],
  ];
  for (const [table, column] of rewrites) {
    if (!tables.includes(table)) continue;
    const columns = db.prepare(`PRAGMA table_info(${table})`).all().map((row) => row.name);
    if (!columns.includes(column)) continue;
    const rows = db.prepare(`SELECT rowid AS id, ${column} AS blob FROM ${table} WHERE ${column} IS NOT NULL`).all();
    for (const row of rows) {
      const next = renameBlob(row.blob);
      if (next === null || next === row.blob) continue;
      if (apply) db.prepare(`UPDATE ${table} SET ${column} = ? WHERE rowid = ?`).run(next, row.id);
      log("rewrite", `${name}:${table}.${column}`, `rowid=${row.id}`);
    }
  }
  if (tables.includes("messages")) {
    const speakerKind = db.prepare("SELECT COUNT(*) AS n FROM messages WHERE speaker_kind = 'role'").get().n;
    const addresseeKind = db.prepare("SELECT COUNT(*) AS n FROM messages WHERE addressee_kind = 'role'").get().n;
    if (speakerKind + addresseeKind > 0) {
      log("rewrite", `${name}:messages attribution`, `speaker_kind ${speakerKind}, addressee_kind ${addresseeKind} 'role' -> 'hero'`);
      if (apply) {
        // The released schema's UPDATE trigger enumerates the old value and aborts
        // this statement. Drop it, backfill, and recreate it with `hero` — the same
        // dance migration 0011 performs, kept here for installs that run the script
        // before the Worker ever opens the database.
        db.exec("DROP TRIGGER IF EXISTS messages_attribution_validate_update");
        db.exec("UPDATE messages SET speaker_kind = 'hero' WHERE speaker_kind = 'role'");
        db.exec("UPDATE messages SET addressee_kind = 'hero' WHERE addressee_kind = 'role'");
        db.exec(`CREATE TRIGGER messages_attribution_validate_update
BEFORE UPDATE OF speaker_kind, visibility, addressee_kind ON messages
WHEN NEW.speaker_kind NOT IN ('user', 'host', 'hero', 'workflow', 'room', 'system')
  OR NEW.visibility NOT IN ('public', 'private', 'room_control', 'legacy_execution')
  OR (NEW.addressee_kind IS NOT NULL AND NEW.addressee_kind NOT IN ('host', 'room', 'hero', 'graph', 'workflow'))
BEGIN
    SELECT RAISE(ABORT, 'message_attribution_invalid');
END;`);
      }
    }
  }
  db.close();
}

log(apply ? "done" : "done (nothing written; re-run with --apply)");
