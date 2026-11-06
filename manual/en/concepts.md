# Concepts

The UI is small, but a few words mean something exact. Once these are clear, the rest behaves
predictably.

## Project

A Project is a folder on disk plus its configuration: default permission mode, models, enabled
skills and MCP servers. The same repository can be mounted as several Projects without interference.

## Session

A continuous conversation, owned by a Project. A Session stores messages, branches, checkpoints and
artifacts. Start a new Session to change topic: history and context do not leak between them.

A Session can have several **branches**: start a new one from any historical message and the original
branch stays untouched.

## Turn and Run

- **Turn**: one message you send.
- **Run**: one execution of that Turn. A Turn can produce several Runs (retries, delegated children).

A Run is a state machine:

```
queued → running → waiting_for_approval → running → succeeded
                 ↘ waiting_children  ↗
                 ↘ waiting_delegation_admit
       any state → cancelled / interrupted / failed
```

The status pill in the UI is exactly this state, and the Run Inspector shows which event moved it.

## Approval

When a tool call exceeds what the current permission mode allows, the Run parks in
`waiting_for_approval` until you decide:

- **Allow once** — this call only
- **Always allow** — this class of call, recorded with its scope
- **Reject** — the Run receives the rejection and usually adjusts or wraps up

The prompt shows the tool name, its risk class and a preview of the arguments. **The Worker enforces
permission before the tool runs**; the model is not asked to behave.

## Attention

Anything that needs you lands in the Attention panel: pending approvals, unanswered questions,
delegations waiting for admission. It is cross-session, so switching away does not lose it.

## Delegation

A Run can spawn child Runs for subtasks (for example "investigate three directions in parallel").
The parent sits in `waiting_children`; child results flow back when they settle. Delegation is scoped
to the Session, and late or cross-scope child results are dropped.

A **background** delegation does not block your composer: you can keep chatting, and you get notified
when it finishes.

## Artifact

Files a tool produces (screenshots, generated reports, fetched pages) are recorded as artifacts and
can be previewed or downloaded. They belong to a Session and are cleaned up with it.

## Skill and MCP

- **Skill**: a reusable procedure plus scripts, read by the model when relevant.
- **MCP**: a Model Context Protocol server that gives the model extra tools (databases, external APIs).

Both are managed in settings, enabled per Project or globally.

## One boundary worth remembering

**The Worker is the only authority.** The UI is its client: state is read from the Worker, changes go
to the Worker. Reloading the page, switching browsers, or closing the UI does not stop a running Run.

That is also why **the UI is read-only while the Worker is unreachable**: it does not queue your
writes for later replay, it tells you it cannot write right now. Silent replay would produce state you
cannot predict.
