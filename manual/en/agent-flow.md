# Agent Flow orchestration

A task that fits in one conversation belongs in chat. A long task with steps, dependencies and
re-runs belongs in an **Agent Flow**: a task graph that says what depends on what, what may run in
parallel, what the budget is, and what counts as done.

An Agent Flow is resumable. A crash, a machine reboot or a cancel does not throw away work that has
already left the graph.

## What it is made of

```
FlowDefinition (declarative task DAG)
   ↓ publish
AgentFlowVersion (immutable revision: roles, skills, models, permissions frozen)
   ↓ enable on a Project, then start
Anchor Run (the meta-Run: orchestration only, never calls a Provider)
   ↓ one per task
child Runs (the layer that does the work, with their own budget, approvals and output contract)
```

Three consequences:

- **Orchestration and execution are separate**: the meta-Run is a state machine, so it can be tested
  and reasoned about on its own
- **Every task is its own child Run**: approvals, budget, failure and cancellation are recorded per
  task, so a problem points at a task rather than "the run"
- **Versions are immutable**: which definition, which role, which model and permissions produced a
  given run stays answerable

## Defining a flow

```yaml
schemaVersion: 1
id: qc-review
description: quality-check a batch of samples and summarise
budget:
  max_total_tokens: 400000         # required; publish rejects a missing value

inputs:
  samples: { type: path, required: true }
outputs:
  report: { type: artifact }

tasks:
  inspect:
    role: explorer@3               # role references carry a version
    skills: [bio-workspace]
    goal: read {{samples}} and list samples with QC metrics
    depends: []

  stats:
    role: analyst@2
    goal: compute per-sample QC metrics
    depends: [inspect]

  gate:                            # check task: a deterministic command
    type: check
    command: "python -m qc --fail-on low-quality"
    depends: [stats]

  report:
    role: writer@1
    goal: summarise into a report
    depends: [stats]               # same layer as gate, may run in parallel
    next:
      pass: done
      fail: retry

  retry:
    role: analyst@2
    goal: repair low-quality samples and recompute
    depends: [gate]

  done:
    type: terminal
    terminal: { status: success, output: report }
```

### Task fields

| Field | Meaning |
|---|---|
| `role` | `role@version`, resolved at publish time; an unresolvable reference fails the publish |
| `skills` | skills available to this task |
| `goal` | the objective handed to the child Run |
| `depends` | upstream tasks; drives readiness order |
| `type` | `role` (default, spawns a child Run) / `check` (deterministic command gate) / plus a `terminal` endpoint |
| `command` | `check` only: the command to run, through the same policy gate and approvals as a tool call |
| `fanOut` | read-only parallel expansion, `min`/`max` instances, outputs aggregated in order |
| `budget` | overrides this task's token ceiling |
| `next` | conditional edges (`pass`/`fail` → task name) |
| `terminal` | endpoint task: no child Run, completes the flow as `success` |
| `writes` | declares which workspace paths this task mutates (globs). **Scheduling only**, see below |

### Convergence and parallelism

```yaml
convergence:
  - { from: retry, to: stats, max_rounds: 3 }   # back-edge plus a round ceiling

parallelism:
  max: 6                     # ready-set dispatch ceiling: default 10, at most 16 at publish time
  allow_disjoint_writers: true
```

Exceeding `max_rounds` terminalises the flow as `convergence_exceeded` — it never retries forever.

!!! warning "`writes` is scheduling, not a sandbox boundary"
    `writes` decides whether two writing tasks may run in parallel (with `allow_disjoint_writers`,
    they must be pairwise disjoint). **It does not isolate files.** Isolation comes from the sandbox
    (`bwrap`). Never use `writes` as a substitute for permission control.

    A task without `writes` is treated as "the whole workspace" and takes an exclusive lane.

## States

| Level | States |
|---|---|
| Flow (meta-Run) | `pending` → `running` → `completed` / `failed` / `cancelled` / `convergence_exceeded` / `budget_exceeded` |
| Task (node) | `pending` → `running` → `completed` / `failed` / `blocked` / `cancelled` / `interrupted` |

A task is a checkpoint: it is written when the node completes, so a resume never replays finished work.

## Budget

- **The flow's `max_total_tokens` is mandatory**; publish rejects a missing value
- A task's `budget.tokens` can tighten one task's ceiling
- Crossing the flow budget terminalises as `budget_exceeded` and stops unfinished tasks

## Publish and enable

A definition is a draft until two steps complete:

```bash
# 1. publish an immutable revision (runs the real validator)
POST /v1/flows/{flowID}/publish

# 2. enable it on a Project (explicit; never automatic)
PUT /v1/projects/{projectID}/graph-bindings/{flowID}
```

Publishing fails loudly on: an unresolvable `role@version`, an unknown skill, malformed
`check`/`fanOut`/`terminal`/`next` structure, an out-of-range parallelism value, or a back-edge without
a round ceiling.

A flow installed from an Agent Pack is also a **draft**: the installer never publishes and never
enables.

## Running and recovering

- Start it from `/graphs`, or let the model request it inside a Session
- The meta-Run shows up in the Session; open it to see every task's state and output
- Background runs do not block your composer

Recovery semantics:

- After a restart the service scans every non-terminal flow run for the session and resumes it
  (`RecoverSession`)
- Resume continues only unfinished nodes; completed checkpoints are not replayed
- Cancelling writes a `cancel_requested` flag that the orchestrator observes at node boundaries, so an
  executing child Run is not torn apart

## Reading a run

| Question | Where |
|---|---|
| Which tasks are done, running, blocked | the graph activity panel on `/graphs`, and the Run Inspector |
| Why did this task fail | open its child Run: event stream, tool calls, approvals |
| How many tokens were spent | flow total, plus per-child usage |
| Why did a check gate fail | the check task's outcome and its approval record |

## Common failures

| Symptom | Cause |
|---|---|
| Publish fails: `role@version` cannot be resolved | the role was never published, or the version is wrong |
| The run button does nothing | the flow is still a draft, or the Project has no GraphBinding |
| Stuck in `pending` | a dependency cycle, or a failed dependency with no `next` fallback |
| `convergence_exceeded` | the back-edge ceiling is too low, or the repair task never changes its input |
| `budget_exceeded` | the flow budget is too small, or a `fanOut` expanded too far |
| Two writing tasks never run in parallel | `allow_disjoint_writers` is off, or their `writes` overlap |
