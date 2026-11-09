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

- The V1 client is the local Web UI (Next.js static export served by
  `ennogate`). It is a client of the Worker authority, not a separate business
  system.
- If a native Tauri shell is introduced, it stays in this repository and is a
  native client shell over the same Worker authority. It must not create a
  second Session, Attention, approval, or delegation implementation.
- Web and any native shell must share React components, generated Worker API
  types, domain reducers, cache keys, and durable synchronization semantics.
- Differences belong behind transport or native-capability interfaces.
- Scope every request, response, cache row, subscription, and event by account,
  Worker, and connection generation as applicable. Reject late cross-Worker data.
- Offline App state is read-only in V1. Do not queue stale mutations for replay.

## Contracts and Cloud Boundary

- `contracts/openapi.yaml` remains authoritative for Worker business APIs.
  Changing a sampled response shape means updating the handler's sample
  (`ENNOTE_UPDATE_CONTRACT_SAMPLES=1 go test ./internal/api/ -run TestContractSamples`)
  and then `npm run generate:contract-samples`; the sample is what `tsc` checks
  the handler's JSON against, so skipping either step leaves the contract
  unverified for that operation.
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
  `npm install --package-lock-only`) plus `info.version` in
  `contracts/openapi.yaml`, commit as
  `chore: bump version to X.Y.Z (<short summary>)`, and create an annotated tag
  `vX.Y.Z` with a one-line summary. Verify with
  `npm run lint && npm run typecheck && npm test && npm run test:go && npm run build`
  before tagging; run `npm run test:e2e` when the change touches rendering or
  session behavior. `npm run release:prepare` builds the distributable worker
  binaries.
- A tag must be an ancestor of `origin/main` before it is pushed; `release.yml`
  refuses to publish otherwise. Push `main` first, then the tag.
- Do not move or reuse a published tag unless its release produced nothing — no
  npm version, no GitHub Release, no attestation. Otherwise cut a new version.
  Re-pushing a moved tag needs `--force`, and anyone who already fetched it keeps
  a stale copy.

## Release Pipeline

- `npm publish` is asynchronous. The registry accepts the upload, answers that the
  package is being processed, and makes the version visible minutes later. Any
  step that verifies a publish must poll with a bounded timeout. A single check
  races the registry and fails a release whose publish already succeeded.
- Keep the publish step idempotent (skip a version that already exists). It is
  what lets a failed job be re-run to finish the steps that follow it —
  attestation and the GitHub Release.
- `npm publish --provenance` requires `repository.url` in `package.json` to match
  the GitHub repository the workflow runs in, or the registry rejects the upload
  with `E422` after signing has already succeeded. Keep
  `repository`/`homepage`/`bugs` populated.
- Release artifacts are attested: `actions/attest-build-provenance` covers the
  worker binaries and `SHA256SUMS`, npm provenance covers the tarball. Verify a
  downloaded artifact against the repository before trusting it. Do not weaken
  these steps to make a release go through.
- **Recorded exception (2026-11-09).** The source repository is private, and GitHub
  only signs attestations for workflows in a public repository; npm rejects
  `--provenance` from a private source with `E422` ("Unsupported … source
  repository visibility"). The release therefore splits the guarantee instead of
  dropping it: the worker binaries and `SHA256SUMS` are mirrored to the public
  repository `seqyuan/ennote-bin` and attested there (verify with
  `gh attestation verify <file> --repo seqyuan/ennote-bin`), while the npm tarball
  is published **unsigned**. This is the single allowed gap; it is not a precedent
  for weakening other steps. Restore npm provenance by making the source public (or
  moving to GHEC) and deleting `--access public`'s unsigned publish path — see
  `release/README.md`.
- A pipeline message that names a cause must derive it from observed state. A
  diagnostic that guesses — "probably staged 2FA" — sends the maintainer down the
  wrong path. Print the registry's answer and what was actually attempted.
