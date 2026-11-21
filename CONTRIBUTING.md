# Contributing

## Local setup

Go (the version in `go.mod`), `jq`, `gitleaks`, and the Python tools in
`ci/requirements.txt`, then the Git hooks:

* `python3 -m venv .venv`
* `.venv/bin/pip install -r ci/requirements.txt`
* `PATH="$PWD/.venv/bin:$PATH" pre-commit install`

The gates to pass before committing are in the README's Development section.

## Branches

Work goes to `main` only through squash-merged pull requests. The branch
prefix decides the next release's version bump:

* `major/`: a major release, for a change that breaks how the proxy is run or
  configured. Its `CHANGELOG.md` item says what a user must change.
* `feat/`: a minor release, for new behaviour or a new server.
* `fix/`: a patch release, for a defect fix.
* `build/` and `ignore/`: no bump, for CI, documentation, and tooling; the
  change ships with the next release. Dependabot's branches do not bump
  either.

## Cutting a release

1. On an up-to-date `main`, run `sh scripts/next-version.sh`. It needs the
   GitHub CLI (`gh`) to read the squash-merged pull requests, and exits 1
   when no `fix/`, `feat/`, or `major/` pull request has merged since the
   last release.
2. Run the signed `git tag -s vX.Y.Z-rc.N` and `git push` commands it prints.
   The Release workflow refuses a tag that is not annotated, not
   verified-signed, or not on `main`, then publishes a pre-release.
3. Prove the release candidate: download a binary, check it with
   `SHA256SUMS` and `gh attestation verify` (see the README's Releases
   section), and confirm `-version` prints `X.Y.Z`.
4. Run the signed `git tag -s vX.Y.Z` and `git push` commands it printed for
   the promotion. The workflow publishes the release candidate's own files as
   the release, after checking their checksums and attestations.

A release tag cannot be moved or deleted. A release candidate that fails
its proof is left as a pre-release, and the fix ships as `vX.Y.Z-rc.N+1`
from a later `main` commit.
