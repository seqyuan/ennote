# MCP

MCP (Model Context Protocol) servers give the model extra tools: databases, internal APIs, file
indexes, and so on.

Ennote does not have a single "turn MCP on and everything appears" switch. There is a chain:
**Profile → Project binding → Run snapshot → Hero intersection**. Once that chain is clear, "I
configured it but the model cannot see it" stops happening.

## The chain

```
Profile (server identity + immutable connection version)   ← Worker level, or declared in a Project
   ↓ create a binding: pick tools, attach credentials
ProjectBinding (which tools one Project enables)           ← the switch lives here
   ↓ frozen when a Run starts
RunMCPServerSnapshot (this Run's capability set)           ← later binding edits do not touch it
   ↓ narrowed again when work is delegated to a Hero
child tools = binding's selected tools ∩ Hero.allowedTools
```

## Step 1: define the server (Profile)

Settings → MCP → New, with three sources:

| Source | Lives in | Meaning |
|---|---|---|
| `managed` | `~/.ennote/config/mcp.json` | a server you add yourself, Worker level |
| `project_file` | inside the Project directory | travels with the repository; `ProjectScope` pins it to that Project |
| `bundled` | shipped with Ennote | pick from the built-in catalog, no connection config to write |

Each server has **immutable versions** (`ProfileVersion`) recording the transport and its settings:

| Transport | Fields |
|---|---|
| `stdio` | `executable`, `argv`, optional `cwd`, `envLiterals` |
| `streamable_http` | `endpoint`, optional `headerLiterals` |
| `legacy_sse` | `endpoint` |

Changing connection settings creates a new version and old ones stay referenceable, so which settings
a Run used is always recoverable (the digest is recorded in the snapshot).

## Step 2: bind it to a Project

This is **the actual switch**. A binding records:

- `desiredEnabled` — on or off
- `selectedRemoteToolNames` — **only the tools you tick are exposed**; nothing is added automatically
- `credentialRefs` — credential references
- `required` — see below

The same server can be bound differently in different Projects, with different tool subsets. A Project
with no binding sees no tools at all.

## Step 3: frozen when the Run starts

When a Run begins, the Project's enabled bindings resolve into snapshots: server, version, negotiated
protocol, catalog digest, per-tool schema digest, plus `required` and any unavailability reason.

Two rules worth remembering:

- **Idempotent**: approval resume and rewind reuse the existing snapshots, so editing a binding mid-Run
  cannot change what that Run can do
- **Selected tools only**: there is no "while we are at it" augmentation

## What `required` means

| Value | When the server is unreachable |
|---|---|
| `required: true` | **Run start is blocked.** Connectivity is verified even when a warm cache exists |
| `required: false` (default) | An `unavailableReason` snapshot is frozen, the Run proceeds without those tools |

So mark hard dependencies `required` and failures surface immediately, while "nice to have" servers do
not stop a Run from starting. A Run has a maximum server count; exceeding it fails rather than
silently truncating.

## Credentials

References use `scheme:value`:

| Form | Resolution |
|---|---|
| `env:MY_TOKEN` | read from the **Worker process environment** |
| `file:/path/to/secret` | needs an injected resolver; the shipped build does not inject one, so it fails loudly instead of silently |
| `keyring:service/account` | same |

Values are resolved **once** at connection time, never logged, never persisted. stdio servers also get
an environment blacklist, so Ennote's own secrets never reach the child process.

## Network policy

- **Loopback endpoints are always allowed** (local services)
- **Private ranges are rejected by default**: an endpoint resolving to an internal address reports
  `endpoint resolves to a private network address ... requires an explicit network policy`
- HTTP transports **resolve once and pin the IP** against DNS rebinding, do not follow cross-origin
  redirects, and use strict TLS by default

## What a tool looks like inside a Run

The exposed name is `serverSlug__toolName` (for example `mydb__query`). That matters in exactly one
place, and it is easy to get wrong:

!!! warning "Hero allowlists must use the exposed name"
    A Hero's `allowedTools` has to list the **exposed name**. Writing the remote name (`query`) does
    not match, and the child Run silently loses the tool.

    A delegated child sees `binding's selected tools ∩ Hero.allowedTools`. A Host Run (a direct
    conversation, no Hero) does not apply that filter and sees every selected tool of the binding.

A tool's risk class comes from the server's declaration (`readOnlyHint` feeds it), so **approval still
follows your current permission mode**: under Ask (`ask`) a writing MCP tool asks first; under
Read-only (`discuss`) it is refused. MCP is not a way around approvals.

## When a server changes its tool list

On `tools/list_changed`:

- the binding's **future** catalog is marked stale
- **the running Run is unaffected** — its tool set is already frozen and never hot-updates mid-Run

So tools cannot change under a Run in flight, and the next Run lists the catalog again. Because
approval identity is bound to the schema digest, an old approval never gets reused against a new schema.

## Quick troubleshooting

| Symptom | Usually |
|---|---|
| No MCP section in the Run Inspector | the Project has no binding with `desiredEnabled` |
| The server appears but has no tools | `selectedRemoteToolNames` is empty, or the snapshot carries an `unavailableReason` |
| A child Run is missing one tool | the Hero lists the remote name instead of the exposed name |
| The Run will not start, binding/profile unavailable | `required: true` and the server is unreachable |
| Connection refused for a private address | the endpoint is internal and needs an explicit network policy |
| `credential resolver required for file:` | a `file:`/`keyring:` reference; use `env:` instead |

Every Run's server and tool snapshots, and the state of each MCP call (planned / dispatched / completed
/ failed / cancelled), are visible in the Run Inspector.
