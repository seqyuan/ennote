# Ennote Repository Guardrails

These instructions apply to the entire repository. They are durable engineering
constraints, not a replacement for the local roadmap or detailed design plans.

## Authority and Runtime

- The Worker host is the sole authority for Projects, Sessions, messages, Runs,
  Attention, approvals, Role/model/Provider configuration, SQLite history,
  Workspace files, virtual mounts, artifacts, and delegation state.
- `ennoworker` must remain loopback-only. Do not add account, Relay, Connect
  Token, or cloud-service dependencies to its startup or business request paths.
- `ennogate` owns local browser authentication, static UI serving, Worker process
  supervision, and the optional outbound remote connector.
- Ennote must start, compute, persist, and serve localhost/LAN Web without an
  account or Worker Connect Token. Missing/revoked credentials and cloud, DNS,
  Relay, or object-service failures may disable remote access only.
- Cloud account expiry, suspension, or termination must never stop a Worker,
  cancel a Run, delete local data, or disable localhost/LAN Web.

## Client Architecture

- Keep the Tauri App in this repository for V1. It is a native client shell over
  the same Worker authority, not a separate business system.
- Web and Tauri must share React components, generated Worker API types, domain
  reducers, cache keys, and durable synchronization semantics.
- Differences belong behind transport or native-capability interfaces. Do not
  create a second Session, Attention, approval, or delegation implementation.
- Scope every request, response, cache row, subscription, and event by account,
  Worker, and connection generation as applicable. Reject late cross-Worker data.
- Offline App state is read-only in V1. Do not queue stale mutations for replay.

## Contracts and Cloud Boundary

- `contracts/openapi.yaml` remains authoritative for Worker business APIs.
- EnnoCloud may own account/control and Relay envelope contracts, but Relay
  payloads must remain encrypted and opaque to cloud services.
- Do not import EnnoCloud persistence models into Worker Store/domain code or
  make Worker behavior depend on cloud database state.
- Remote qualification must include a real cloud-unavailable/local-available
  path in addition to remote API and event tests.

## Repository Hygiene

- Never commit credentials, Provider keys, passwords, activation/reset codes,
  Connect Token plaintext, signing material, or decrypted user content.
- `/docs/` and `/doc/` are local-only design/history directories in this
  workspace. Do not force-add or push them.
- Keep roadmap priorities in the local roadmap and architecture invariants here.
  Do not treat temporary agent memory as a source of truth.

## Commit and Release Conventions

- Use Conventional Commits with a scoped subject: `feat(chat):`, `fix(worker):`,
  `refactor(settings):`, `perf(chat):`, `test(e2e):`, `docs:`, `chore:`. One
  logical change per commit.
- Scope `git add` to the exact files of that change and commit them separately.
  Never use `git add -A` / `git commit -a` to sweep unrelated workspace state
  (including pre-existing work-in-progress) into a commit.
- Keep coupled files in the same commit so each commit is self-consistent:
  a `contracts/openapi.yaml` change ships with its regenerated
  `lib/worker-api.gen.ts` and the Go/TS code that consumes it. If a commit does
  not typecheck/build on its own, it is not ready to commit.
- The message-timeline projection (`lib/chat-messages.ts`) and its consumers
  (`hooks/useChatController.ts`, `hooks/useSessionStore.ts`) are streaming hot
  paths. Perf/refactor changes there must ship with the unit tests that pin
  reference-preservation semantics (`tests/unit/chat-messages.test.ts`).
- Never commit generated/build artifacts: `worker/`, `out/`,
  `ennoworker/ennogate`, `ennoworker/ennoworker` are gitignored and produced at
  release time.
- Releases: bump `version` in `package.json` (and `package-lock.json` via
  `npm install --package-lock-only`), commit as
  `chore: bump version to X.Y.Z (<short summary>)`, and create an annotated tag
  `vX.Y.Z` with a one-line summary. Verify with
  `npm run lint && npm run typecheck && npm test && npm run test:go && npm run build`
  before tagging; run `npm run test:e2e` when the change touches rendering or
  session behavior. `npm run release:prepare` builds the distributable worker
  binaries.
