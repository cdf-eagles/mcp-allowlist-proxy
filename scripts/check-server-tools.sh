#!/bin/sh
# Check a server's tools against its reviewed lists, through the proxy.
#
# Usage: check-server-tools.sh PROXY SERVER_DIR -- COMMAND [ARG...]
#
# Starts COMMAND behind PROXY with SERVER_DIR/allowlist.txt, asks for the tool
# list over MCP, and reads the proxy's tools/list log line. It fails when:
#   - an allowlisted tool is not offered by the server (renamed or removed);
#   - the server offers a tool that is in neither allowlist.txt nor
#     hidden.txt (a new tool nobody has decided);
#   - a tool in hidden.txt is no longer offered (keep the list exact);
#   - the client sees a different number of tools than the allowlist holds.
# The server must answer tools/list without reaching its backend; point it at
# an address that refuses connections.

set -eu

[ "$#" -ge 4 ] && [ "$3" = "--" ] || {
	echo "usage: check-server-tools.sh PROXY SERVER_DIR -- COMMAND [ARG...]" >&2
	exit 2
}
proxy=$1
dir=$2
shift 3

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

names() { # names FILE: tool names in a list file, sorted
	sed -e 's/#.*//' -e 's/[[:space:]]//g' -e '/^$/d' "$1" | sort
}

names "$dir/allowlist.txt" >"$tmp/allowed"
if [ -f "$dir/hidden.txt" ]; then names "$dir/hidden.txt" >"$tmp/hidden.want"; else : >"$tmp/hidden.want"; fi

# Keep the client side open until the tools/list reply arrives (60 s at most),
# then close it so the proxy and the server exit. The loop reads the proxy's
# output file while the proxy writes it; that is how it sees the reply.
# shellcheck disable=SC2094
{
	printf '%s\n' \
		'{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"check-server-tools","version":"1"}}}' \
		'{"jsonrpc":"2.0","method":"notifications/initialized"}' \
		'{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
	i=0
	while [ "$i" -lt 60 ] && ! grep -q '"id":2[,}]' "$tmp/out" 2>/dev/null; do
		sleep 1
		i=$((i + 1))
	done
} | "$proxy" -allow "$dir/allowlist.txt" -- "$@" >"$tmp/out" 2>"$tmp/err" || true

fail=0
line=$(grep 'tools/list: ' "$tmp/err" | tail -1 || true)
if [ -z "$line" ]; then
	echo "FAIL: no tools/list reply; proxy and server stderr follow" >&2
	cat "$tmp/err" >&2
	exit 1
fi
echo "$line"

# A paged tool list would leave the later pages unchecked.
if grep -q '"nextCursor"' "$tmp/out"; then
	echo "FAIL: the server pages its tool list; this check reads one page only" >&2
	exit 1
fi

printf '%s\n' "$line" | sed -n 's/.* hidden \[\([^]]*\)\].*/\1/p' | tr ' ' '\n' | sed '/^$/d' | sort >"$tmp/hidden.got"
missing=$(printf '%s\n' "$line" | sed -n 's/.*not offered by the server: \[\([^]]*\)\].*/\1/p')
if [ -n "$missing" ]; then
	echo "FAIL: allowlisted but not offered by the server: $missing" >&2
	fail=1
fi
new=$(comm -23 "$tmp/hidden.got" "$tmp/hidden.want" | tr '\n' ' ')
if [ -n "$new" ]; then
	echo "FAIL: offered but in neither allowlist.txt nor hidden.txt: $new" >&2
	fail=1
fi
gone=$(comm -13 "$tmp/hidden.got" "$tmp/hidden.want" | tr '\n' ' ')
if [ -n "$gone" ]; then
	echo "FAIL: in hidden.txt but no longer offered: $gone" >&2
	fail=1
fi
want_n=$(wc -l <"$tmp/allowed" | tr -d ' ')
got_n=$(printf '%s\n' "$line" | sed -n 's/.*tools\/list: \([0-9][0-9]*\) allowed.*/\1/p')
if [ "$got_n" != "$want_n" ]; then
	echo "FAIL: the client saw ${got_n:-no} tools; the allowlist holds $want_n" >&2
	fail=1
fi
if [ "$fail" -eq 0 ]; then
	echo "PASS: $want_n allowed and $(wc -l <"$tmp/hidden.want" | tr -d ' ') hidden tools match the server's tool list"
fi
exit "$fail"
