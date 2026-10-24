# mcp-allowlist-proxy: repository conventions

Repository-specific rules only. Rules shared by every repository (backlog,
evidence, delivery, and public-repository writing rules) live outside this
repository and are not repeated here.

## Scope and design

- An MCP proxy (stdio to the client; stdio or Streamable HTTP to the server)
  that enforces a tool allowlist. It must fail closed:
  anything it cannot read as one JSON object is dropped or refused, never
  passed through unexamined.
- It limits the client, not the server; the README's threat model says so
  and must stay accurate when behaviour changes.
- Standard library only; no third-party Go modules. Builds use
  `CGO_ENABLED=0`; macOS (arm64 and amd64) is the target, Linux is built and
  tested in CI.
- Decode JSON-RPC by exact key through `map[string]json.RawMessage`; never
  decode protocol fields into structs, whose case-insensitive matching would
  let a `Method` or `Name` key mean something different to the proxy than to
  the server.
- Never log tool arguments or results; tool names and counts only.
- Keep `go.mod` on the latest stable Go release.

## Tests

- A new test must fail against the code before the fix.
- The command is tested end to end with the test binary as a helper server
  (`TestHelperServer`).
- Gates before staging: `go test -race ./...` (coverage at least 90
  percent), `golangci-lint run ./...` for `GOOS=linux` and `GOOS=darwin` with
  the committed `.golangci.yml` (golangci-lint built with the Go version in
  `go.mod`), `go tool -modfile=ci/tools/go.mod govulncheck ./...`,
  `shellcheck -s sh` on every shell script, `yamllint --strict .`,
  `sh scripts/test-check-server-tools.sh bin/mcp-allowlist-proxy`, and
  `pre-commit run --all-files`.

## CI

- `.github/workflows/ci.yaml` is mandatory and follows the vault's Repository
  and CI Standards: every gate runs once, gitleaks scans the full history on
  an unshallow clone (`--log-opts="HEAD"`), and workflow files are checked
  by schema and actionlint. Actions are pinned to a commit SHA with the tag
  in a comment; tools and the runner image are pinned to a version. Tool
  versions live in `ci/requirements.txt`, `.pre-commit-config.yaml`, and
  `ci/tools/go.mod`, where Dependabot updates them; only a release download
  verified by checksum (gitleaks) is pinned in the workflow's `env`. Never
  use `@latest` or a floating major.
- Branches: `main` only; work branches are squash merged into `main` by pull
  request.

## Servers

- `servers/<name>/` holds `allowlist.txt` (allowed), `hidden.txt` (reviewed
  and refused), `tools.lock` (hashes of each allowed tool's description,
  input schema, and other fields, written by
  `scripts/check-server-tools.sh -w`), and `Dockerfile`, a pin that is never
  built: one `FROM` line naming the server image by tag and digest.
- Every tool the pinned image offers is in exactly one of the two lists;
  `scripts/check-server-tools.sh` enforces it in CI. A server bump decides
  each new tool explicitly, with a dated comment, and regenerates
  `tools.lock`; the new text of every changed tool is read before the
  regenerated file is committed.
- No site-specific values (hosts, accounts, secret paths) in this
  repository; they live in the client's configuration and the deployment.
- Node.js is not a dependency of this repository: servers are checked and
  run as their images.
