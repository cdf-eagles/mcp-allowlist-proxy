# Changelog

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
