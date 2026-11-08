# Skills

A skill is **documentation for the model**: a procedure, some conventions, and scripts if it needs
them. It executes nothing. It tells the model how this kind of work is done here, at the moment that
work comes up.

The difference from a tool is worth stating up front:

| | Tool | Skill |
|---|---|---|
| What it is | an executable capability | readable instructions |
| Who runs it | the Worker | the model, after reading it |
| Permission | governed by tool policy and approvals | not a permission mechanism, just text |

## What a skill looks like

At its smallest, a skill is **a directory with one `SKILL.md`**:

```
skills/bio-workspace/
  SKILL.md          # the text the model reads
  skill.json        # optional metadata
  scripts/
    qc.sh
```

`skill.json`, when present:

```json
{
  "id": "bio-workspace",
  "version": "0.1.0",
  "description": "Run QC and exploration in a local bioinformatics workspace",
  "prompt": "SKILL.md",
  "allowedTools": ["read", "ls", "grep", "find", "bash"]
}
```

A skill with no `skill.json` works too: `SKILL.md`-only skills from the pi ecosystem are supported and
Ennote synthesises the manifest.

**Write `description` as "when to use this"** — it is what the model uses to decide whether to read the
file at all.

## How the model sees skills

When a Run starts, Ennote renders a bounded catalogue (at most 16 KiB) into the system prompt:

```
- skill `bio-workspace`: Run QC and exploration in a local bioinformatics workspace. Location: `/skills/bio-workspace/SKILL.md`
- category `scRNA`: single-cell procedures. Location: `/skills/scRNA/category.md`
```

Then:

1. the model opens the path with `read` when it needs it
2. `/skills` is a **read-only mount** inside the sandbox; a skill cannot modify itself
3. relative paths inside a skill resolve against the directory holding that `SKILL.md`

One consequence is easy to trip over:

!!! warning "Skills depend on the `read` tool"
    If the current policy does not allow `read`, the whole skill catalogue is skipped (recorded as
    disabled) and the model sees none of it. Usually fine under Read-only (`discuss`); keep it in mind
    when you tighten the tool allowlist.

## Hierarchy and precedence

Skills can be grouped:

```
skills/
  bio-workspace/
    SKILL.md
  scRNA/                 # a category needs category.md
    category.md
    pseudotime/
      SKILL.md
```

Name conflicts resolve by source priority, **lower number wins**:

| Source | Location | Priority |
|---|---|---|
| user | `~/.ennote/skills/` (plus any custom root you add) | 0 |
| builtin | the `skills/` directory shipped in the npm package | 1 |

So overriding a built-in skill means dropping a same-named directory in your user root — no need to
touch the package.

## Three ways to get a skill

| Way | How |
|---|---|
| Built in | shipped with the package, available immediately |
| Write one | put it in `~/.ennote/skills/<name>/`, or have the model generate it with the `create_skill` tool |
| Install from the marketplace | Settings → Skills → search and install; underneath it runs `npx skills add`, with global or project scope |

Marketplace-installed skills can be checked for updates, updated and removed individually; skills you
wrote yourself are just files and are not part of that management.

!!! note "Project scope needs a trusted workspace"
    Installing into project scope requires the workspace to be trusted. Without trust, only global
    scope is available.

## Disable and enable

```bash
# disable one skill by its relative path
PATCH /v1/skills/disabled/{relPath}
```

A disabled skill never enters the catalogue, so the model will not read it. This is safer than
deleting: the files stay, and re-enabling is one call.

## Referencing skills from Heroes and Flows

| Where | Field | Meaning |
|---|---|---|
| Hero | `skills.entries[]` | each `{ skillId, mode }`, with `mode` either `preload` or `available` |
| Flow task | `skills: [name, ...]` | skills available to that task's child Run |

`preload` suits rules that must be read every time; `available` suits procedures the model should read
only when relevant.

## When skills freeze

The skill set a Run uses is frozen with that Run: the directory contents are materialised under
`sessions/snapshots/skills/sha256-<digest>/` and each skill's path and digest are recorded.

So **editing a skill cannot change a Run already in flight**; the next Run picks up the new content. It
also keeps "which version of the skill produced this answer" answerable afterwards.

## Writing a good skill

1. **Write `description` as a trigger condition**, not marketing copy — it is the model's filter
2. **Say what the skill is *not* for** — boundaries prevent misuse better than steps do
3. **Use relative paths** for scripts and assets, never absolute ones
4. **Keep permissions out of it** — a skill is not policy; whether a tool may run is decided by the
   permission mode and the Hero
5. When it grows, split it into a `category.md` plus subdirectories so the model can narrow down

## Troubleshooting

| Symptom | Usually |
|---|---|
| The model never mentions a skill | the skill is disabled, or the policy denies `read` and the whole catalogue was skipped |
| The model does not read your skill | `description` does not say when to use it, or a same-named skill overrode it |
| Behaviour did not change after an edit | you edited while a Run was in flight (frozen); start a new Session or Run |
| A script inside the skill is not found | an absolute path, or not relative to the `SKILL.md` directory |
| Marketplace install fails | needs network; project scope also needs a trusted workspace |
