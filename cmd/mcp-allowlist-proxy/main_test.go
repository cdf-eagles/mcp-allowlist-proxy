package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHelperServer is not a test: run starts the test binary itself as the
// child server with MCP_ALLOWLIST_PROXY_HELPER set, and this function plays a
// minimal MCP server that answers tools/list and echoes tools/call names.
func TestHelperServer(*testing.T) {
	mode := os.Getenv("MCP_ALLOWLIST_PROXY_HELPER")
	if mode == "" {
		return
	}
	if mode == "exit3" {
		os.Exit(3)
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			os.Exit(4)
		}
		switch m["method"] {
		case "tools/list":
			fmt.Printf(`{"jsonrpc":"2.0","id":%v,"result":{"tools":[{"name":"getMonitor"},{"name":"deleteMonitor"}]}}`+"\n", m["id"])
		case "tools/call":
			name := m["params"].(map[string]any)["name"]
			fmt.Printf(`{"jsonrpc":"2.0","id":%v,"result":{"content":[{"type":"text","text":"called %v"}]}}`+"\n", m["id"], name)
		}
	}
	os.Exit(0)
}

func helperArgs(t *testing.T, allow string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "allow.txt")
	if err := os.WriteFile(path, []byte(allow), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"-allow", path, "--", os.Args[0], "-test.run=^TestHelperServer$"}
}

func TestRunEndToEnd(t *testing.T) {
	t.Setenv("MCP_ALLOWLIST_PROXY_HELPER", "serve")
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"getMonitor"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"deleteMonitor"}}`,
	}, "\n") + "\n"
	var out, errs bytes.Buffer
	if code := run(helperArgs(t, "getMonitor\n"), strings.NewReader(in), &out, &errs); code != 0 {
		t.Fatalf("run = %d, stderr %s", code, errs.String())
	}
	got := out.String()
	if strings.Contains(got, "called deleteMonitor") || !strings.Contains(got, "called getMonitor") {
		t.Errorf("stdout = %s", got)
	}
	if strings.Count(got, `"name":"deleteMonitor"`) != 0 || !strings.Contains(got, `"name":"getMonitor"`) {
		t.Errorf("tools/list not filtered: %s", got)
	}
	if !strings.Contains(got, `"id":3,"error"`) && !strings.Contains(got, `"error":{"code":-32602`) {
		t.Errorf("no refusal for id 3: %s", got)
	}
	if !strings.Contains(errs.String(), "1 tools allowed") || !strings.Contains(errs.String(), `refused tools/call "deleteMonitor"`) {
		t.Errorf("stderr = %s", errs.String())
	}
}

func TestRunPassesExitCode(t *testing.T) {
	t.Setenv("MCP_ALLOWLIST_PROXY_HELPER", "exit3")
	var out, errs bytes.Buffer
	if code := run(helperArgs(t, "getMonitor\n"), strings.NewReader(""), &out, &errs); code != 3 {
		t.Errorf("run = %d, want the server's 3", code)
	}
}

func TestRunUsageAndErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(bad, []byte("not valid!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(dir, "good.txt")
	if err := os.WriteFile(good, []byte("getMonitor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		args []string
		code int
	}{
		"no args":         {nil, 2},
		"no command":      {[]string{"-allow", good}, 2},
		"no allow":        {[]string{"--", "true"}, 2},
		"bad flag":        {[]string{"-nope"}, 2},
		"bad allowlist":   {[]string{"-allow", bad, "--", "true"}, 1},
		"missing command": {[]string{"-allow", good, "--", filepath.Join(dir, "no-such-server")}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errs bytes.Buffer
			if code := run(c.args, strings.NewReader(""), &out, &errs); code != c.code {
				t.Errorf("run = %d, want %d; stderr %s", code, c.code, errs.String())
			}
		})
	}
}

func TestRunVersion(t *testing.T) {
	var out, errs bytes.Buffer
	if code := run([]string{"-version"}, strings.NewReader(""), &out, &errs); code != 0 || strings.TrimSpace(out.String()) != version {
		t.Errorf("run -version = %d, %q", code, out.String())
	}
}

func TestRunRemote(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		if m["method"] == "tools/call" {
			calls = append(calls, m["params"].(map[string]any)["name"].(string))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"tools":[{"name":"getMonitor"},{"name":"deleteMonitor"}]}}`, m["id"])
	}))
	defer srv.Close()

	dir := t.TempDir()
	allow := filepath.Join(dir, "allow.txt")
	tok := filepath.Join(dir, "tok")
	if err := os.WriteFile(allow, []byte("getMonitor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tok, []byte("tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"deleteMonitor"}}` + "\n"
	var out, errs bytes.Buffer
	if code := run([]string{"-allow", allow, "-url", srv.URL + "/mcp", "-token-file", tok}, strings.NewReader(in), &out, &errs); code != 0 {
		t.Fatalf("run = %d; stderr %s", code, errs.String())
	}
	if strings.Contains(out.String(), `"name":"deleteMonitor"`) || !strings.Contains(out.String(), `"code":-32602`) || len(calls) != 0 {
		t.Errorf("stdout %s, server calls %v", out.String(), calls)
	}
	if !strings.Contains(errs.String(), "relaying to "+srv.URL) || strings.Contains(errs.String(), "tok\n") {
		t.Errorf("stderr = %s", errs.String())
	}

	if err := os.Chmod(tok, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-allow", allow, "-url", srv.URL, "-token-file", tok}, strings.NewReader(""), &out, &errs); code != 1 {
		t.Errorf("a world-readable token file gave %d, want 1", code)
	}
	if err := os.Chmod(tok, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"-allow", allow, "-url", "http://example.net/mcp", "-token-file", tok}, strings.NewReader(""), &out, &errs); code != 1 {
		t.Errorf("plain http to a remote host gave %d, want 1", code)
	}
	for _, args := range [][]string{
		{"-allow", allow, "-url", srv.URL},
		{"-allow", allow, "-token-file", tok},
		{"-allow", allow, "-url", srv.URL, "-token-file", tok, "--", "true"},
	} {
		if code := run(args, strings.NewReader(""), &out, &errs); code != 2 {
			t.Errorf("run %v = %d, want usage 2", args, code)
		}
	}
}
