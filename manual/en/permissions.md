# Models and permissions

Ennote is not tied to one model vendor: anything speaking an OpenAI-compatible API works.

## Configure a Provider

Settings → Models → New Provider takes three things:

| Field | Meaning |
|---|---|
| Base URL | e.g. `https://api.openai.com/v1`, or a local Ollama / LM Studio |
| API Key | stored under `~/.ennote/config/` with mode `0600`, never written to logs |
| Models | type the ids manually or fetch the list from the provider |

Local inference is allowed: provider probes may reach loopback. The **web fetch** tool is the
opposite — it rejects loopback and private ranges, because it fetches untrusted content and must not
be turned into a probe of your own machine.

## Choosing a model

Each Session picks its model and thinking effort. A Project can set defaults that new Sessions
inherit.

The configuration a Run froze is recorded in its snapshot, so "which model produced this, under what
settings" stays answerable afterwards.

## Three permission modes

The three modes differ in exactly one thing: **what happens to a tool that is not read-only** —
writing a file, running a command, fetching the web, delegating. The name is the answer; the value in
parentheses is what the configuration stores.

| Mode | A non-read-only tool | Suits |
|---|---|---|
| Read-only (`discuss`) | is refused | reading and reasoning |
| Ask (`ask`) | waits for your approval | the default recommendation |
| Auto (`auto`) | runs | a task you already trust |

Switch it any time from the run-configuration row under the composer. Switching affects later tool
calls; it never replays what already happened.

## A Role's permission ceiling

When work is delegated to a Role, the child Run gets **the stricter of the two**:

```
child permission = min(parent Run's mode, the Role's permission ceiling)
```

So a parent in Ask (`ask`) delegating to an Auto (`auto`) Role still runs at `ask`, and a Read-only
(`discuss`) Role stays read-only even under an `auto` parent. The Permission ceiling dropdown in the
Role editor is these same three values, ordered by strictness.

When a mode cannot be expressed as a built-in profile (a restricted policy, or one this build does not
know), the child is pinned to the strictest built-in mode rather than inheriting the looser side: when
the comparison cannot prove safety, fail toward stricter.

## Approvals and delegation admission

Two places ask for your decision:

- **Tool approval**: a call exceeds what the current mode allows
- **Delegation admission**: a delegation that spends your quota or your time can require approval
  before it is admitted

Decisions are written to the event stream, so afterwards you can see who approved what, when, and with
which arguments.

## Sandbox

The default mode is `bwrap` (bubblewrap) on **every platform**: filesystem isolation with only the
workspace writable.

!!! warning "On macOS you need bubblewrap, or the degraded mode"
    macOS does not ship `bwrap`. Without it, execution tools fail with
    `bubblewrap is required for sandbox mode bwrap` rather than silently degrading. Either install
    bubblewrap, or set `ENNOTE_SANDBOX=none` explicitly to use the degraded mode.

`ENNOTE_SANDBOX=none` is the degraded mode: the Worker statically analyses shell commands and rejects
anything that could assemble a protected path (unquoted globs, brace expansion, variable expansion,
command substitution, `$'...'`), and refuses access to `~/.ennote`. It is still static analysis —
`sh -c "$(cat x)"` and friends are only stopped by a real sandbox.
