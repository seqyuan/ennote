# LAN and security

By default Ennote listens on `127.0.0.1` only. This page covers how to open it up safely and what
becomes your responsibility once you do.

## The default shape

```
browser ──HTTP──> ennogate (127.0.0.1:45131) ──HTTP──> ennoworker (loopback)
                  · password, cookie                · the only authority
                  · serves the static UI            · never listens publicly
```

The Worker **only ever listens on loopback**. It takes no part in accounts, tokens or cloud logic, so
a cloud failure cannot affect it.

## Exposing it to the LAN

```bash
ENNOTE_HOSTNAME=0.0.0.0 ENNOTE_ALLOW_LAN=1 \
ENNOTE_TLS_CERT=/path/cert.pem ENNOTE_TLS_KEY=/path/key.pem \
ennote start
```

Three gates, all required:

1. `ENNOTE_ALLOW_LAN=1` — binding a non-loopback address must be explicit, otherwise startup is
   **refused**
2. TLS — without a certificate you must also set `ENNOTE_ALLOW_INSECURE_LAN=1` (plaintext means the
   password and a 7-day cookie cross the network in the clear; only for a trusted network, briefly)
3. Host allowlist — see below

## Host validation (since 0.6.0)

Every request's `Host` header is checked, to stop DNS rebinding: an attacker can point their own
domain at `127.0.0.1`, and without this check a fresh install could be taken over before you ever log
in.

Rules:

- **IP literals are always allowed**: `http://192.168.1.5:45131` works
- **loopback names are allowed**: `localhost`, `127.0.0.1`, `[::1]`
- **hostnames must be listed explicitly**, otherwise the request gets 403:

```bash
ENNOTE_HOSTNAME=0.0.0.0 ENNOTE_ALLOW_LAN=1 \
ENNOTE_GATE_ALLOWED_HOSTS=myhost.local,ennote.lan \
ennote start
```

!!! warning "After upgrading to 0.6.0"
    If you used a hostname such as `http://myhost.local:45131` for LAN access, you will now get a 403.
    Either browse by IP, or put that hostname in `ENNOTE_GATE_ALLOWED_HOSTS`. This is a deliberate
    tightening, not a fault.

## Credentials and keys

- Provider API keys live under `~/.ennote/config/` with mode `0600` and never reach the logs
- The local UI password is stored with bcrypt
- Session cookies carry `Secure` under TLS
- Login sessions live in `~/.ennote/config/sessions-<port>.json` (mode `0600`), so restarting the gate
  does not log you out. The file holds HMAC digests keyed by the password hash, never a replayable
  token
- Changing the password logs every browser out: `ennote passwd`, `ennote passwd --reset` and deleting
  `auth.json` by hand all do it, and so does setting the same password again (bcrypt uses a new salt
  each time). A long-lived connection that is already open — an SSE event stream — keeps running
  until it closes
- Every tool that fetches an external URL refuses private ranges, link-local addresses and cloud
  metadata endpoints

## Files and artifacts

- A writing tool call can only land inside the Project workspace (the root is canonicalised, so a
  symlink cannot walk you out)
- `~/.ennote` is a protected path: in degraded mode a shell command that references it is refused
- Artifacts and spill files are mode `0600`

## The cloud is optional

Ennote runs indefinitely with no network. Remote access, accounts and Relay are optional: when they
are unavailable, local startup, computation, persistence and LAN access are unaffected. That is a
constraint, not a best effort.

## Checklist before exposing Ennote to a LAN

- [ ] Use TLS; avoid `ENNOTE_ALLOW_INSECURE_LAN`
- [ ] List only the hostnames you need in `ENNOTE_GATE_ALLOWED_HOSTS`, never a wildcard
- [ ] Make sure your router does not forward port 45131 to the internet — Ennote is not designed for
      public exposure
- [ ] Glance at `~/.ennote/logs/ennote.log` now and then
