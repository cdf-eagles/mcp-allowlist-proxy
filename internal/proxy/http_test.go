package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cdf-eagles/mcp-allowlist-proxy/internal/allowlist"
)

// fakeServer is a minimal Streamable HTTP MCP server that records what it
// receives.
type fakeServer struct {
	mu       sync.Mutex
	calls    []string // tools/call names received
	methods  []string
	headers  []http.Header
	deleted  bool
	sse      bool   // answer requests as an event stream
	session  string // session id to assign on initialize
	status   int    // non-zero: answer every POST with this status
	arrayOut bool   // answer tools/list as a one-element array
}

func (f *fakeServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodDelete {
			f.deleted = true
			w.WriteHeader(http.StatusOK)
			return
		}
		f.headers = append(f.headers, r.Header.Clone())
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = io.WriteString(w, `{"error":"unauthorized"}`)
			return
		}
		var msg map[string]any
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Errorf("server got invalid JSON: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		method, _ := msg["method"].(string)
		f.methods = append(f.methods, method)
		id, hasID := msg["id"]
		if !hasID {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result string
		switch method {
		case "initialize":
			if f.session != "" {
				w.Header().Set("Mcp-Session-Id", f.session)
			}
			result = `{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"fake","version":"1"}}`
		case "tools/list":
			result = `{"tools":[{"name":"getMonitor"},{"name":"deleteMonitor"},{"name":"pauseMonitor"}]}`
		case "tools/call":
			name := msg["params"].(map[string]any)["name"].(string)
			f.calls = append(f.calls, name)
			result = `{"content":[{"type":"text","text":"called ` + name + `"}]}`
		default:
			result = `{}`
		}
		idJSON, _ := json.Marshal(id)
		reply := `{"jsonrpc":"2.0","id":` + string(idJSON) + `,"result":` + result + `}`
		if f.arrayOut && method == "tools/list" {
			reply = "[" + reply + "]"
		}
		if f.sse {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, ": keep-alive\n\nevent: message\nid: 1\ndata: "+reply+"\n\nevent: other\ndata: ignored\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}
}

func runHTTP(t *testing.T, f *fakeServer, input string) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	allow, err := allowlist.Parse(strings.NewReader("getMonitor\npauseMonitor\nlistMonitors\n"))
	if err != nil {
		t.Fatal(err)
	}
	var client, logs bytes.Buffer
	out := &lockedBuffer{b: &client}
	p := New(allow, out, log.New(&lockedBuffer{b: &logs}, "", 0))
	c, err := NewHTTPClient(srv.URL+"/mcp", "s3cret-token", "test/1")
	if err != nil {
		t.Fatal(err)
	}
	// Feed the input as a real client does: after each request, wait for its
	// reply before sending the next line.
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- p.ServeHTTP(context.Background(), pr, c) }()
	for _, line := range strings.SplitAfter(input, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if _, err := io.WriteString(pw, line); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["id"] != nil && m["method"] != nil {
			idJSON, _ := json.Marshal(m["id"])
			deadline := time.Now().Add(5 * time.Second)
			for !strings.Contains(out.String(), `"id":`+string(idJSON)) && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
		}
	}
	_ = pw.Close()
	if err := <-done; err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	return &client, &logs
}

type lockedBuffer struct {
	mu sync.Mutex
	b  *bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

const session = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"getMonitor"}}
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"deleteMonitor"}}
`

func checkSession(t *testing.T, f *fakeServer, client, logs *bytes.Buffer) {
	t.Helper()
	out := client.String()
	if strings.Contains(out, "deleteMonitor\"}") || strings.Contains(out, `"name":"deleteMonitor"`) {
		t.Errorf("a hidden tool reached the client: %s", out)
	}
	for _, want := range []string{`"name":"getMonitor"`, `"name":"pauseMonitor"`, "called getMonitor", `"code":-32602`} {
		if !strings.Contains(out, want) {
			t.Errorf("client output lacks %s: %s", want, out)
		}
	}
	if len(f.calls) != 1 || f.calls[0] != "getMonitor" {
		t.Errorf("server received calls %v, want only getMonitor", f.calls)
	}
	for _, h := range f.headers {
		if h.Get("Authorization") != "Bearer s3cret-token" || h.Get("User-Agent") != "test/1" {
			t.Errorf("request headers %v", h)
		}
	}
	if strings.Contains(logs.String(), "s3cret-token") {
		t.Errorf("token logged: %s", logs.String())
	}
}

func TestHTTPJSONSession(t *testing.T) {
	f := &fakeServer{session: "sess-1"}
	client, logs := runHTTP(t, f, session)
	checkSession(t, f, client, logs)
	if f.headers[0].Get("Mcp-Session-Id") != "" || f.headers[0].Get("Mcp-Protocol-Version") != "" {
		t.Errorf("initialize carried session headers: %v", f.headers[0])
	}
	last := f.headers[len(f.headers)-1]
	if last.Get("Mcp-Session-Id") != "sess-1" || last.Get("Mcp-Protocol-Version") != "2025-06-18" {
		t.Errorf("later request headers: %v", last)
	}
	if !f.deleted {
		t.Error("the session was not ended with DELETE")
	}
}

func TestHTTPEventStreamAndArray(t *testing.T) {
	f := &fakeServer{sse: true}
	client, logs := runHTTP(t, f, session)
	checkSession(t, f, client, logs)
	if f.deleted {
		t.Error("DELETE sent although the server assigned no session")
	}
	if strings.Contains(client.String(), "ignored") {
		t.Error("a non-message event reached the client")
	}

	f = &fakeServer{arrayOut: true}
	client, logs = runHTTP(t, f, session)
	checkSession(t, f, client, logs)
}

func TestHTTPErrorsBecomeJSONRPCErrors(t *testing.T) {
	f := &fakeServer{status: http.StatusUnauthorized}
	client, logs := runHTTP(t, f, `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`+"\n"+`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
	var reply map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(client.Bytes()), &reply); err != nil {
		t.Fatalf("client got %q: %v", client.String(), err)
	}
	e, ok := reply["error"].(map[string]any)
	if !ok || reply["id"].(float64) != 7 || !strings.Contains(e["message"].(string), "HTTP 401") {
		t.Errorf("reply = %v", reply)
	}
	if !strings.Contains(logs.String(), "notifications/initialized") {
		t.Errorf("the failed notification was not logged: %q", logs.String())
	}
}

func TestHTTPUnreachable(t *testing.T) {
	allow, _ := allowlist.Parse(strings.NewReader("getMonitor\n"))
	var client, logs bytes.Buffer
	p := New(allow, &client, log.New(&logs, "", 0))
	c, err := NewHTTPClient("http://127.0.0.1:1/mcp", "t", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ServeHTTP(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"), c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.String(), "server unreachable") {
		t.Errorf("client = %s", client.String())
	}
}

func TestHTTPUnexpectedReplies(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"wrong type": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>")
		},
		"bad array": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "[not json")
		},
		"empty body": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			allow, _ := allowlist.Parse(strings.NewReader("getMonitor\n"))
			var client, logs bytes.Buffer
			p := New(allow, &client, log.New(&logs, "", 0))
			c, _ := NewHTTPClient(srv.URL, "t", "")
			if err := p.ServeHTTP(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"), c); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{
				"wrong type": "unexpected reply type",
				"bad array":  "no response to this request",
				"empty body": "no response to this request",
			}[name]
			if !strings.Contains(client.String(), want) || !strings.Contains(client.String(), `"id":1`) {
				t.Errorf("client = %s, want an error reply containing %q", client.String(), want)
			}
		})
	}
}

func TestNewHTTPClientChecksTheURL(t *testing.T) {
	for u, ok := range map[string]bool{
		"https://kuma-mcp.example.net/mcp": true,
		"http://127.0.0.1:3000/mcp":        true,
		"http://localhost:3000/mcp":        true,
		"http://[::1]:3000/mcp":            true,
		"http://kuma-mcp.example.net/mcp":  false,
		"http://localhost.evil.net/mcp":    false,
		"ftp://x/mcp":                      false,
		"https://user:pw@x/mcp":            false,
		"https://x/mcp#frag":               false,
		"https:///mcp":                     false,
		"://bad":                           false,
	} {
		if _, err := NewHTTPClient(u, "t", ""); (err == nil) != ok {
			t.Errorf("NewHTTPClient(%q) error = %v, want ok=%v", u, err, ok)
		}
	}
	for _, tok := range []string{"", "two words", "line\nbreak", "tab\there"} {
		if _, err := NewHTTPClient("https://x/mcp", tok, ""); err == nil {
			t.Errorf("token %q was accepted", tok)
		}
	}
	c, err := NewHTTPClient("https://x/mcp?key=v", "t", "")
	if err != nil || c.Display != "https://x/mcp" || c.Endpoint != "https://x/mcp?key=v" {
		t.Errorf("Display %q Endpoint %q err %v", c.Display, c.Endpoint, err)
	}
}

func TestReadTokenFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte("  tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if tok, err := ReadTokenFile(good); err != nil || tok != "tok" {
		t.Errorf("ReadTokenFile(good) = %q, %v", tok, err)
	}
	open := filepath.Join(dir, "open")
	if err := os.WriteFile(open, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o640); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{open, empty, dir, link, filepath.Join(dir, "missing")} {
		if _, err := ReadTokenFile(p); err == nil {
			t.Errorf("ReadTokenFile(%s) succeeded", filepath.Base(p))
		}
	}
}

func serve(t *testing.T, h http.HandlerFunc, input string) (string, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	allow, _ := allowlist.Parse(strings.NewReader("getMonitor\n"))
	var client, logs bytes.Buffer
	p := New(allow, &lockedBuffer{b: &client}, log.New(&lockedBuffer{b: &logs}, "", 0))
	c, err := NewHTTPClient(srv.URL+"/mcp", "tok", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ServeHTTP(context.Background(), strings.NewReader(input), c); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	return client.String(), logs.String()
}

func TestHTTPRedirectIsNotFollowed(t *testing.T) {
	var followed bool
	h := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/elsewhere" {
			followed = true
		}
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}
	out, _ := serve(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n")
	if followed || !strings.Contains(out, "HTTP 307") {
		t.Errorf("followed=%v client=%s", followed, out)
	}
}

func TestHTTPAcceptedRequestGetsAnError(t *testing.T) {
	h := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) }
	out, _ := serve(t, h, `{"jsonrpc":"2.0","id":5,"method":"tools/list"}`+"\n"+`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "no response to this request") {
		t.Errorf("client = %s, want one error reply for id 5 only", out)
	}
}

func TestHTTPEventStreamEdges(t *testing.T) {
	h := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// CRLF line ends, an empty priming event, a multi-line data payload,
		// and a last event with no blank line after it.
		_, _ = io.WriteString(w, "\ufeffdata:\r\n\r\nevent: message\r\ndata: {\"jsonrpc\":\"2.0\",\r\ndata: \"id\":1,\"result\":{\"tools\":[{\"name\":\"getMonitor\"},{\"name\":\"deleteMonitor\"}]}}")
	}
	out, logs := serve(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n")
	if !strings.Contains(out, `"name":"getMonitor"`) || strings.Contains(out, "deleteMonitor") || strings.Contains(out, "error") {
		t.Errorf("client = %s", out)
	}
	if strings.Contains(logs, "dropped") {
		t.Errorf("an empty event was treated as a message: %s", logs)
	}
}

func TestHTTPEventStreamWithoutResponse(t *testing.T) {
	h := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
	}
	out, _ := serve(t, h, `{"jsonrpc":"2.0","id":9,"method":"tools/list"}`+"\n")
	if !strings.Contains(out, "notifications/progress") || !strings.Contains(out, "no response to this request") {
		t.Errorf("client = %s", out)
	}
}

func TestHTTPOversizedEvent(t *testing.T) {
	h := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := "data: " + strings.Repeat("a", 1<<20) + "\n"
		for i := 0; i < 70; i++ {
			_, _ = io.WriteString(w, chunk)
		}
	}
	out, _ := serve(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n")
	if !strings.Contains(out, "event larger than the message limit") {
		t.Errorf("client = %.200s", out)
	}
}

func TestHTTPInitializeBeforeOthers(t *testing.T) {
	// The client pipelines its messages; the session from initialize must
	// still reach every later request.
	f := &fakeServer{session: "sess-9"}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	allow, _ := allowlist.Parse(strings.NewReader("getMonitor\n"))
	var client bytes.Buffer
	p := New(allow, &lockedBuffer{b: &client}, log.New(io.Discard, "", 0))
	c, _ := NewHTTPClient(srv.URL, "tok", "")
	if err := p.ServeHTTP(context.Background(), strings.NewReader(session), c); err != nil {
		t.Fatal(err)
	}
	if f.methods[0] != "initialize" {
		t.Fatalf("first method %q", f.methods[0])
	}
	for i, h := range f.headers[1:] {
		if h.Get("Mcp-Session-Id") != "sess-9" {
			t.Errorf("request %d (%s) without the session id", i+1, f.methods[i+1])
		}
	}
}

func TestHTTPCancelReturnsWithStdinOpen(t *testing.T) {
	f := &fakeServer{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	allow, _ := allowlist.Parse(strings.NewReader("getMonitor\n"))
	p := New(allow, io.Discard, log.New(io.Discard, "", 0))
	c, _ := NewHTTPClient(srv.URL, "tok", "")
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.ServeHTTP(ctx, pr, c) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ServeHTTP after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeHTTP did not return after cancel while stdin stayed open")
	}
}
