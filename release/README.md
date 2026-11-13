# Release helpers

## `ennote-bin-attest.yml`

The binaries users download live in the **public** repository
[`seqyuan/ennote-bin`](https://github.com/seqyuan/ennote-bin), not here.

Why: the launcher (`bin/ennote.js`) defaults to `seqyuan/ennote-bin` for first-run
downloads, so the binaries are mirrored there and attested there. The source
repository is public (since 2026-11-13), so it is also attested directly by
`release.yml`.

`release.yml` therefore does two things after building and testing:

1. creates the release here (the tag, commit, and bytes), and
2. mirrors the binaries + `SHA256SUMS` into `ennote-bin` and dispatches
   `ennote-release` there.

This file is the workflow that belongs in `ennote-bin` at
`.github/workflows/attest.yml`: it downloads the assets it just received,
attests them, and records the source revision in the release notes.

## npm provenance

`npm publish` runs **with** `--provenance` (since 2026-11-13, when
`seqyuan/ennote` was made public): the registry accepts a Sigstore bundle for the
tarball, so the npm tarball and the binaries it downloads are both signed — the
binaries here and in `ennote-bin`, the tarball via npm provenance.

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
