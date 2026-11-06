# Troubleshooting

Each entry says how to confirm the problem and how to fix it. Check here before digging through logs.

## The UI does not open

```bash
ennote status
```

- `running: false` → the service is not up: `ennote start`. If startup fails, read
  `~/.ennote/logs/ennote.log`
- `running: true` but the browser cannot reach it → confirm you are using the URL and port that
  `status` reports (default `http://127.0.0.1:45131`)

## Port already in use

```
Port 45131 is already in use on 127.0.0.1
```

Pick another port: `ennote start --port 4200`. Or check whether the previous Ennote is still running.

## Forgot the local password

```bash
ennote passwd          # set a new one
ennote passwd --reset  # remove it, so the next start asks for setup
```

## The UI says the Worker is unreachable

The banner `Worker unreachable — Ennote is read-only until it returns` means every write is refused.
That is the offline read-only policy: writes are never queued for replay.

Confirm whether the Worker is really gone:

```bash
curl -s http://127.0.0.1:45131/api/worker/v1/health/ready
```

- Any HTTP response (even 401/428) → the Worker is alive; the UI retries within 5 seconds and recovers
- Connection refused → it is not running: `ennote stop && ennote start`

!!! note "No network does not mean unavailable"
    Read-only is decided by whether the **Worker answers**, not by whether the browser has internet. In
    airplane mode the local Worker still works and the UI stays writable.

## 403 when reaching the LAN by hostname

That is the Host validation added in 0.6.0. Browse by IP, or allow the hostname explicitly:

```bash
ENNOTE_GATE_ALLOWED_HOSTS=myhost.local ennote start
```

See [LAN and security](lan-security.md).

## The first install could not download

With no network, or without access to GitHub, `ennote` falls back to a complete build already on disk:

```bash
ennote status --json   # shows binaryVersion and binaryFallback
```

The log carries `warning: failed to install Ennote x.y.z binaries (...); using cached a.b.c`. If no
complete build exists, the error names the URL it tried and the cache directory; either place the
binaries manually or point `ENNOTE_RELEASES_BASE` at a mirror.

## A command is refused

In the degraded mode (`ENNOTE_SANDBOX=none`), the Worker statically analyses shell commands and
refuses:

- unquoted globs (`*`, `?`) — they may expand to any path
- brace expansion (`{a,b}`), variable expansion (`$VAR`), command substitution (`$(...)`), ANSI-C
  quoting (`$'...'`)
- anything that assembles a `~/.ennote` path, including split across quotes, escapes or empty quote
  pairs

Use a literal path, or enable the `bwrap` sandbox (the default on Linux).

## Starting over

```bash
ennote stop
mv ~/.ennote ~/.ennote.bak   # back it up first
ennote start
```

## Collecting diagnostics

```bash
ennote status --json
tail -n 200 ~/.ennote/logs/ennote.log
curl -s http://127.0.0.1:45131/api/health
```

Those three together locate most problems. When filing an issue, include `binaryVersion` from
`ennote status --json` — a version mismatch sends the investigation down the wrong path.
