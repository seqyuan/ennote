---
title: Getting started
---

# Ennote

A local-first, auditable agent runtime. A Worker process runs on your machine and a browser is
the interface. Every Session, message, Run, approval and file change stays in a local SQLite
database.

No account. No network needed to start working. The cloud only exists for optional remote access.

## How it differs

- **Data stays local**: SQLite and files under `~/.ennote/`. Deleting the app does not touch them.
- **The process is auditable**: every event, tool call and approval of a Run is recorded, so you
  can answer "how did this answer come about".
- **Permissions are enforced at the tool boundary**, not requested from the model in a prompt.
- **Cloud outages cannot break local use**: an expired account, a Relay failure or broken DNS does
  not stop the Worker from starting, computing or serving the local UI.

## Requirements

| Item | Requirement |
|---|---|
| OS | macOS or Linux (x64 / arm64) |
| Node.js | 22 or newer |
| Browser | any modern browser with SSE |
| Network | needed for the first install; not needed afterwards |

## Install

```bash
npm install -g @seqyuan/ennote
ennote start
```

`start` downloads the Worker binaries into `~/.ennote/bin/` (skipped when cached), starts `ennogate`
on `127.0.0.1:45131`, and opens the UI. The first visit asks you to set a password. That password
protects the local UI; it is not a cloud account.

!!! note "Installing without network access"
    If the download fails, `ennote` falls back to the newest complete build already in
    `~/.ennote/bin/` and prints a warning instead of exiting. `ennote status --json` reports which
    build is actually serving:

    ```bash
    ennote status --json
    # { "running": true, "binaryVersion": "0.6.0", "binaryFallback": false, ... }
    ```

## Five-minute tour

1. **Create a Project** — a Project is a folder. Pick an empty directory or an existing repository.
2. **Create a Session** — a Session is one continuous conversation. Start a new one to change topic.
3. **Send a message**, for example "read this repository and tell me where the entry point is".
4. **Answer the permission prompt** — read-only tools are usually allowed outright; writing files,
   running commands and network access ask for approval, showing the exact arguments first.
5. **Open the Run Inspector** to see the event stream, tool calls, token usage and timings.

## Where data lives

```
~/.ennote/
  bin/                      downloaded Worker binaries (per version)
  config/                   models, provider credentials, policies, MCP (0600)
  agents/heroes/             hero definitions
  agents/graphs/            graph definitions
  skills/                   skills
  projects/<project>/sessions/   one SQLite database per Session
  data/                     projection databases: catalog.db, usage.db
  cache/                    caches
  runtime/                  Worker handshake (worker-state.json)
  spill/                    oversized tool output written to disk
  logs/ennote.log           launcher and process log
```

Move the directory to migrate; delete it to start over.

## Upgrade

```bash
ennote stop
npm install -g @seqyuan/ennote@latest
ennote start
```

Schema migrations are versioned and run automatically. Upgrading never deletes existing data.

## Commands

| Command | Purpose |
|---|---|
| `ennote start` | start the service (default `127.0.0.1:45131`) |
| `ennote stop` | stop it |
| `ennote status` | whether it runs, the URL, the binary actually serving |
| `ennote passwd` | reset or remove the local UI password |
| `ennote pack inspect <path>` | show what an Agent Pack would install, without changing anything |

Reaching it from another machine? See [LAN and security](lan-security.md).
