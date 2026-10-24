#!/bin/sh
# Check a server's tools against its reviewed lists, through the proxy.
#
# Usage: check-server-tools.sh [-w] PROXY SERVER_DIR -- COMMAND [ARG...]
#
# Starts COMMAND behind PROXY with SERVER_DIR/allowlist.txt, asks for the tool
# list over MCP, and reads the proxy's tools/list log line. It fails when:
#   - an allowlisted tool is not offered by the server (renamed or removed);
#   - the server offers a tool that is in neither allowlist.txt nor
#     hidden.txt (a new tool nobody has decided);
#   - a tool in hidden.txt is no longer offered (keep the list exact);
#   - the client sees a different number of tools than the allowlist holds;
#   - an allowed tool's description, input schema, or any other field differs
#     from its entry in SERVER_DIR/tools.lock, or has no entry there.
# The server must answer tools/list without reaching its backend; point it at
# an address that refuses connections.
#
# With -w, and only when the checks above other than tools.lock pass, it
# writes SERVER_DIR/tools.lock from the tools the client sees. Needs jq 1.7 or
# later.

set -eu

write=0
if [ "${1:-}" = "-w" ]; then
	write=1
	shift
fi
[ "$#" -ge 4 ] && [ "$3" = "--" ] || {
	echo "usage: check-server-tools.sh [-w] PROXY SERVER_DIR -- COMMAND [ARG...]" >&2
	exit 2
}
case $(jq --version 2>/dev/null || true) in
jq-1.[7-9]* | jq-[2-9]*) ;;
*)
	echo "check-server-tools.sh: jq 1.7 or later is required" >&2
	exit 2
	;;
esac
proxy=$1
dir=$2
shift 3

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

names() { # names FILE: tool names in a list file, sorted
	sed -e 's/#.*//' -e 's/[[:space:]]//g' -e '/^$/d' "$1" | sort
}

sha256() { # sha256: SHA-256 of standard input, as bare hex
	if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi | cut -d' ' -f1
}

entries() { # entries FILE: one line per tool in the proxy's tools/list reply
	# Each line is the tool's name, then the SHA-256 of its description, of its
	# inputSchema, and of every other field, each as canonical JSON (keys
	# sorted, no extra whitespace).
	jq -S -c 'select(.id == 2) | .result.tools[]? | (.name), (.description), (.inputSchema), del(.name, .description, .inputSchema)' "$1" |
		while IFS= read -r name && IFS= read -r desc && IFS= read -r schema && IFS= read -r other; do
			name=${name#\"}
			name=${name%\"}
			printf '%s description=%s inputSchema=%s otherFields=%s\n' "$name" \
				"$(printf '%s' "$desc" | sha256)" "$(printf '%s' "$schema" | sha256)" "$(printf '%s' "$other" | sha256)"
		done | LC_ALL=C sort
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

if ! jq -e 'select(.id == 2) | .result.tools | arrays' "$tmp/out" >/dev/null 2>&1; then
	echo "FAIL: the proxy's output holds no tools/list result to hash" >&2
	exit 1
fi
entries "$tmp/out" >"$tmp/lock.got"
lock=$dir/tools.lock
if [ "$write" -eq 1 ]; then
	if [ "$fail" -eq 0 ]; then
		{
			echo "# SHA-256 of each allowed tool's description, input schema, and other fields."
			echo "# Written by scripts/check-server-tools.sh -w; do not edit by hand."
			cat "$tmp/lock.got"
		} >"$tmp/lock.new"
		cp "$tmp/lock.new" "$lock"
		echo "WROTE: $lock ($(wc -l <"$tmp/lock.got" | tr -d ' ') tools)"
	fi
elif [ ! -f "$lock" ]; then
	echo "FAIL: $lock does not exist; write it with -w" >&2
	fail=1
else
	sed -e 's/#.*//' -e '/^[[:space:]]*$/d' "$lock" >"$tmp/lock.want"
	awk '
	FILENAME == ARGV[1] {
		if (NF != 4 || $2 !~ /^description=[0-9a-f]+$/ || $3 !~ /^inputSchema=[0-9a-f]+$/ || $4 !~ /^otherFields=[0-9a-f]+$/) {
			printf "FAIL: malformed line in tools.lock: %s\n", $1
			bad = 1
			next
		}
		if ($1 in want) {
			printf "FAIL: tools.lock lists %s twice\n", $1
			bad = 1
		}
		want[$1] = $0
		next
	}
	{
		seen[$1] = 1
		if (!($1 in want)) {
			printf "FAIL: allowed tool %s has no entry in tools.lock\n", $1
			bad = 1
			next
		}
		split(want[$1], w, " ")
		for (i = 2; i <= 4; i++) {
			if (w[i] != $i) {
				split($i, p, "=")
				printf "FAIL: the %s of tool %s differs from tools.lock\n", p[1], $1
				bad = 1
			}
		}
	}
	END {
		for (n in want) {
			if (!(n in seen)) {
				printf "FAIL: tools.lock lists %s, which the client no longer sees\n", n
				bad = 1
			}
		}
		exit bad
	}' "$tmp/lock.want" "$tmp/lock.got" >&2 || fail=1
fi
if [ "$fail" -eq 0 ] && [ "$write" -eq 0 ]; then
	echo "PASS: $want_n allowed and $(wc -l <"$tmp/hidden.want" | tr -d ' ') hidden tools match the server's tool list, and the allowed tools match tools.lock"
fi
exit "$fail"
