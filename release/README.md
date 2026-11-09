# Release helpers

## `ennote-bin-attest.yml`

The binaries users download live in the **public** repository
[`seqyuan/ennote-bin`](https://github.com/seqyuan/ennote-bin), not here.

Why: a release asset in a private repository is authenticated, so the launcher
(`bin/ennote.js`) cannot fetch it on a first `npx` run, and GitHub only produces
build attestations for workflows in a public repository.

`release.yml` therefore does two things after building and testing:

1. creates the release here (private audit trail: tag, commit, bytes), and
2. mirrors the binaries + `SHA256SUMS` into `ennote-bin` and dispatches
   `ennote-release` there.

This file is the workflow that belongs in `ennote-bin` at
`.github/workflows/attest.yml`: it downloads the assets it just received,
attests them, and records the source revision in the release notes.

## npm provenance

`npm publish` runs **without** `--provenance` while this source repository is
private: the registry rejects a Sigstore bundle whose source repository is not
public (`E422 … Unsupported GitHub Actions source repository visibility`). The
npm tarball is therefore unsigned; the binaries it downloads are attested in
`ennote-bin`, which is what a user actually executes.

To restore npm provenance, make `seqyuan/ennote` public (or move to GHEC) and put
`--provenance` back on the publish step. AGENTS.md records this as the one allowed
gap and not a precedent.

Setup (once):

```bash
# 1. Add the workflow to the public repository
mkdir -p /tmp/ennote-bin/.github/workflows
cp release/ennote-bin-attest.yml /tmp/ennote-bin/.github/workflows/attest.yml

# 2. Give this repository a fine-grained PAT with contents:write on
#    seqyuan/ennote-bin, stored as the BIN_REPO_TOKEN secret.
gh secret set BIN_REPO_TOKEN --repo seqyuan/ennote
```

Verify a downloaded binary:

```bash
gh attestation verify ennogate-linux-x64 --repo seqyuan/ennote-bin
```
