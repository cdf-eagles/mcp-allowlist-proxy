# mcp-allowlist-proxy

A proxy for Model Context Protocol (MCP) servers that lets through only the
tools named in an allowlist. It speaks MCP over standard input and output to
the client, and reaches the server either as a child process (stdio) or as a
remote endpoint (Streamable HTTP). It removes every other tool from the
server's tool lists, and answers a `tools/call` for any other tool with a
JSON-RPC error without forwarding it.

Use it in front of an MCP server that has no read-only mode or tool filter of
its own, when the client must not be able to call some of its tools. The
guard sits outside the client, so it holds whatever the client or the model
decides to do.

## Threat model

The proxy limits the **client**: what an MCP client, and the model driving
it, can see and call. It does not limit the **server**.

- The server runs with whatever credentials it is given. If those grant full
  access to the backend (as they do for Uptime Kuma, which has a single
  administrator account), a compromised or malicious server release can use
  all of it, and the proxy cannot stop it. The server image and everything in
  it are the trust boundary; pin it by digest (see `servers/uptime-kuma`) and
  review every bump.
- In remote mode the allowlist is enforced where the proxy runs, not at the
  endpoint. Anyone holding the endpoint's bearer token can call every tool
  the server offers directly. Treat that token like the backend credential.
- The proxy does not inspect tool arguments or results. An allowed tool can
  do everything that tool does: `updateMonitor` can rewrite a monitor
  completely. Some tools reach further than their names suggest: a
  notification tool (`addNotification`, `updateNotification`) can point
  alerts and webhooks at any URL, which can carry data out, and a monitor
  tool (`createMonitor`, `updateMonitor`) can make the backend probe any host
  it can reach, internal ones included. Choose the allowlist with that in
  mind.
- Anyone who can run commands as the proxy's user can read the token file
  and talk to the endpoint directly. The proxy is a guard on an MCP session,
  not a sandbox.

## Build

Go 1.27.3 or later, no other dependencies:

```sh
go build -o bin/mcp-allowlist-proxy ./cmd/mcp-allowlist-proxy
```

`bin/` is ignored by git.

## Usage

```sh
mcp-allowlist-proxy -allow FILE -- COMMAND [ARG...]
mcp-allowlist-proxy -allow FILE -url URL -token-file FILE
```

**Stdio mode** starts `COMMAND` with the proxy's environment and relays
between it and the client. The server's standard error passes through. The
proxy exits with the server's exit status and forwards SIGINT, SIGTERM, and
SIGHUP to it.

**Remote mode** POSTs each client message to `URL` (Streamable HTTP) with
`Authorization: Bearer <token>`, and relays the reply, whether a JSON body or
an event stream. `URL` must be `https://` (plain `http://` only for a
loopback host) and must carry no user information; redirects are never
followed. The token file must be a regular file that grants no access to
group or others. `initialize` is answered before anything else is sent;
other requests run concurrently, so a slow tool call does not hold up the
next one. Every request gets an answer: the server's response, or a JSON-RPC
error when the connection fails, the server answers with an HTTP error or a
redirect, or its reply holds no response. The proxy carries a session id and
protocol version when the server assigns them, and ends the session when the
client closes or on SIGINT, SIGTERM, or SIGHUP.

In both modes the proxy logs to standard error: its version and allowlist
size at start, each refused call (tool name only, quoted and truncated, never
arguments), and the result of each `tools/list` filter when it changes,
including any allowlisted tool the server no longer offers. It never logs the
token.

## Allowlist and hidden list

`allowlist.txt` holds one tool name per line; `#` starts a comment, blank
lines are ignored. Names are matched exactly and case sensitively. An invalid
name, a name listed twice, or a file that names no tools stops the proxy at
start. A tool not on the allowlist is hidden and refused, so a tool added by a
new server release stays blocked until someone lists it.

Each server directory also keeps `hidden.txt`, the tools that were reviewed
and deliberately left off. The proxy does not read it; CI does
(`scripts/check-server-tools.sh`), and fails when the pinned server offers a
tool in neither file, when an allowlisted tool disappears, or when a hidden
tool is no longer offered. A server upgrade therefore cannot merge until
every tool it offers has been decided.

## What the proxy checks

- Every message in either direction must be a single JSON object. JSON-RPC
  batches from the client are refused with an error; non-JSON lines are
  dropped and logged.
- Messages are decoded by exact key (`method`, `params`, `name`), never by
  case-insensitive matching, and a repeated key keeps its last value, as
  JavaScript's `JSON.parse` does. Client messages are re-encoded from the
  decoded form, so the server receives exactly what the proxy checked.
- Every server message whose result carries a tools list is filtered,
  whatever its id or other keys; request ids are matched by value (`1`,
  `1.0`, and `1e0` are one id).

## Servers

Each directory under `servers/` holds an allowlist, a hidden list, and the
pin of the server they were reviewed against.

### servers/uptime-kuma

For [mcp-uptime-kuma](https://github.com/DavidFuchs/mcp-uptime-kuma), run as
its container image. `Dockerfile` is a pin, never built: it records
`davidfuchs/mcp-uptime-kuma` **0.12.0** by tag and digest, which is the image
CI checks the lists against and the one to deploy.

The allowlist admits reading, and creating, changing, pausing, and resuming
monitors, notifications, tags, and maintenance windows (24 of the server's 35
tools); every delete and every status-page and Docker-host change is in
`hidden.txt`.

The intended deployment runs the image on a server with its Streamable HTTP
transport (`-t streamable-http`), its bearer token (`MCP_AUTH_TOKEN`) set, and
TLS and source-address limits in a reverse proxy in front of it. The proxy
runs on each client machine in remote mode. Client configuration (Claude
desktop app, `claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "uptime-kuma": {
      "command": "/absolute/path/to/mcp-allowlist-proxy/bin/mcp-allowlist-proxy",
      "args": [
        "-allow", "/absolute/path/to/mcp-allowlist-proxy/servers/uptime-kuma/allowlist.txt",
        "-url", "https://kuma-mcp.example.net/mcp",
        "-token-file", "/absolute/path/to/bearer-token-file"
      ]
    }
  }
}
```

The server's HTTP transport rate-limits each source address to 100 requests
in 15 minutes; behind a reverse proxy without `TRUST_PROXY`, every client
shares one budget.

Upgrading: Dependabot opens one pull request per image release. Read the
release notes, decide every new tool in `allowlist.txt` or `hidden.txt` until
CI passes, merge, then deploy the same tag and digest and restart the client.

## CI

`.github/workflows/ci.yaml` runs on every pull request and push to `main`,
and weekly:

- **Lint**: golangci-lint for Linux and macOS, shellcheck, yamllint, and the
  pre-commit hooks, which include the workflow and Dependabot schemas,
  actionlint, ripsecrets, and detect-private-key (each tool runs once).
- **Secret scan**: gitleaks over the history of the checked-out commit (on a
  pull request, `main` plus the pull request), from a checksum-verified
  release, on an unshallow clone.
- **Test and build**: `go test -race` with at least 90 percent coverage,
  govulncheck, and builds for macOS (arm64 and amd64) and Linux.
- **Server allowlists**: for each server, a check that its image is pinned by
  tag and full digest, then `scripts/check-server-tools.sh` against that
  image, run with no network, no capabilities, and limited processes and
  memory. A paged tool list fails the check.

Every action is pinned to a commit, and every tool CI installs to an exact
version; the runner image is pinned to Ubuntu 24.04, whose own packages are
not. Tool versions live where Dependabot updates them weekly: the Python
tools in `ci/requirements.txt`, actionlint and the golangci-lint version in
`.pre-commit-config.yaml`, and govulncheck in `ci/tools/go.mod`, a separate
module that stays out of the build. gitleaks is pinned with its checksum in
the workflow, and the weekly run's **Tool drift** job opens an issue when it
falls behind its latest release.

## Development

Gates before committing: `go test -race ./...` (coverage at least 90
percent), `golangci-lint run ./...` for `GOOS=linux` and `GOOS=darwin` with
the committed `.golangci.yml`, `go tool -modfile=ci/tools/go.mod govulncheck ./...`,
`shellcheck -s sh` on every shell script, `yamllint --strict .`, and
`pre-commit run --all-files`. Run `pre-commit install` once per clone: it also
installs a push hook that runs gitleaks over the commits being pushed.

## License

BSD 3-Clause; see `LICENSE`.
