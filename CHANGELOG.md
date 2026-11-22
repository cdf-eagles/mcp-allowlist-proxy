# Changelog

- 2026-11-21
  - The `servers/uptime-kuma` pin moves from mcp-uptime-kuma 0.15.0 to 0.17.0. 0.16.0 renamed tools and merged the pause and resume pairs, so the allowlist now names `listMaintenanceWindows`, `createNotification`, `createTag`, `createMaintenanceWindow`, `updateMaintenanceWindow`, `setMonitorActive`, and `setMaintenanceWindowActive`, and `getHeartbeats` is gone. Of the 30 tools 0.17.0 adds, the allowlist admits the read-only ones except `listAPIKeys` and `listPM2Processes`, plus `updateTag` and `testNotification`; the rest (history and database resets, settings, incidents, proxies, remote browsers, API keys, `testChrome`) are in `hidden.txt`. 34 of the server's 67 tools are allowed, and `tools.lock` holds the new hashes. Found internally from backlog item 627.
  - `scripts/check-server-tools.sh` reads the tools/list reply's own `nextCursor`, as the proxy does, instead of searching the reply's text for it: mcp-uptime-kuma 0.17.0's paged list tools declare a `nextCursor` property in their output schemas, which the text search took for a paged tool list. `scripts/test-check-server-tools.sh` covers both cases. Found internally from backlog item 627.
  - Releases: a signed release candidate tag `vX.Y.Z-rc.N` on `main` runs the new Release workflow, which tests, builds the macOS (arm64 and amd64) and Linux (amd64) binaries with `-version` set from the tag, and publishes a pre-release with a `SHA256SUMS` file and build provenance attestations; a signed `vX.Y.Z` tag on the same commit publishes the candidate's own files, checked against both, without rebuilding. Found internally from backlog item 573.
  - `scripts/next-version.sh` proposes the next version from the branch prefixes of the pull requests merged since the last release (`fix/` patch, `feat/` minor, `major/` major), and `CONTRIBUTING.md` covers the branch prefixes and cutting a release.
- 2026-11-19
  - Dependabot raised the `servers/uptime-kuma` pin to mcp-uptime-kuma 0.15.0, which adds the read-only `getMonitorTypeFields` tool and moves type-specific monitor settings into a `typeFields` argument of `createMonitor` and `updateMonitor`. The allowlist admits `getMonitorTypeFields`, since those two tools no longer describe the fields themselves, and `tools.lock` holds the new hashes; every tool's other fields changed because 0.15.0 adds Model Context Protocol (MCP) annotations (read-only, destructive, and idempotent hints) to all of them.
- 2026-11-18
  - A Snyk job in continuous integration (CI) tests the Python tools pinned in `ci/requirements.txt`, which Snyk's repository import skips, and fails on any high or critical issue, a scan error, or a missing `SNYK_TOKEN` secret. The Snyk CLI 1.1308.4 is downloaded from its GitHub release and verified by its checksum; found internally from backlog item 572.
- 2026-11-15
  - SonarCloud analyses the proxy from continuous integration (CI) instead of its automatic analysis, so the analysis now carries test coverage and the quality gate's coverage condition on new code applies. The new `SonarCloud` job runs pysonar, pinned in `ci/requirements.txt`, reading its token from Amazon Web Services (AWS) Systems Manager Parameter Store through OpenID Connect (OIDC). Found internally from backlog item 582.

- 2026-11-14
  - Added `servers/snyk-cli` for the Model Context Protocol (MCP) server built into the Snyk command-line interface (CLI), with the `lite` tool profile and folder trust disabled. The allowlist admits the dependency scan, the static analysis scan, and `snyk_version`, and hides sign-in, sign-out, folder trust, and feedback. Found internally from backlog item 548.
  - The Snyk CLI is pinned as a checksum-verified release in the continuous integration (CI) workflow's `env` (`SNYK_VERSION` 1.1308.4 and `SNYK_SHA256`), which the Server allowlists job downloads and checks, because the CLI is a self-contained binary and its images carry no version tags. Found internally from backlog item 548.
  - The weekly Tool drift job calls the Tool drift workflow v1.1.1, which also checks the `SNYK_VERSION` pin against the latest Snyk CLI release.
  - Added `servers/pysnyk-mcp` for pysnyk-mcp, a read-only MCP server for the Snyk API, pinned to its upstream image by commit tag and digest; 22 of its 23 tools are allowed, and `snyk_list_members`, which returns member email addresses, is hidden. The Snyk API needs an Enterprise plan: on the Free and Team plans its token reaches only the user lookup. Found internally from backlog item 548.
  - The README shows badges for CI on `main`, pre-commit, the Go version, and the license, and this changelog starts, filled in from the repository's history.

- 2026-11-12
  - Builds with Go 1.27.4, which fixes GO-2026-6859 (Common Vulnerabilities and Exposures entry CVE-2026-97054), a crash of HTTP/1 servers in the standard library `net/http` package. Found internally from backlog item 547.

- 2026-11-09
  - The weekly Tool drift job calls the Tool drift workflow v1.1.0, which also reads the Go version in `go.mod` and opens an issue when a newer patch release of its line exists or the line reaches its end of life. Found internally from backlog item 382.

- 2026-11-03
  - Dependabot raised check-jsonschema from 0.38.2 to 0.39.0, as both the pre-commit hook and the CI Python tool.

- 2026-10-24
  - The CI secret scan downloads gitleaks 8.30.1, up from 8.21.2, verified by its checksum, and the download accepts only HTTPS with Transport Layer Security (TLS) 1.2 or later, so a redirect cannot drop it to plain HTTP. Found internally from backlog item 381.

- 2026-10-23
  - CI fails when an allowed tool's description, input schema, or any other field changes: `servers/<name>/tools.lock` holds a SHA-256 (Secure Hash Algorithm, 256-bit) hash of each, and `scripts/check-server-tools.sh` compares them with the tool list the client sees through the proxy. `scripts/test-check-server-tools.sh` tests that check against a fake server.
  - Dependabot raised the `servers/uptime-kuma` pin to mcp-uptime-kuma 0.12.1, which adds optional database-monitor fields to `createMonitor` and `updateMonitor` and no tool; the list headers and the README name 0.12.1.
  - Tool versions moved where Dependabot updates them: `ci/requirements.txt`, `.pre-commit-config.yaml`, and `ci/tools/go.mod`. pre-commit now runs ripsecrets and detect-private-key, and a pre-push hook runs gitleaks over the commits being pushed. Found internally from backlog items 357, 356, 361, and 380.
  - The secret scan covers the history of the checked-out commit, a weekly Tool drift job reports when the gitleaks pin falls behind, checkouts no longer keep their credentials, and the build moved to Go 1.27.3.
  - Dependabot raised golangci-lint to 2.15.0.

- 2026-10-20
  - First release: an MCP proxy over standard input and output that passes through only the tools on an allowlist and fails closed, reaching the server as a child process or as a Streamable HTTP endpoint. `servers/uptime-kuma` pins the mcp-uptime-kuma image (0.12.0, by digest) and admits 24 of its 35 tools, refusing every delete, status-page, and Docker-host change.
  - CI lints, tests with at least 90 percent coverage, runs govulncheck, scans the history with gitleaks, and checks each pinned server's tools against its lists. The macOS lint step installs golangci-lint for the runner and lints for macOS.
