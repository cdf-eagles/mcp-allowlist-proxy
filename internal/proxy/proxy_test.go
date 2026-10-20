package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/cdf-eagles/mcp-allowlist-proxy/internal/allowlist"
)

func newTestProxy(t *testing.T) (*Proxy, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	allow, err := allowlist.Parse(strings.NewReader("getMonitor\nlistMonitors\npauseMonitor\n"))
	if err != nil {
		t.Fatal(err)
	}
	var client, logs bytes.Buffer
	return New(allow, &client, log.New(&logs, "", 0)), &client, &logs
}

// lines splits newline-terminated output into decoded objects.
func lines(t *testing.T, b *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimRight(b.String(), "\n"), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("output line %q is not JSON: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func clientSend(t *testing.T, p *Proxy, input string) *bytes.Buffer {
	t.Helper()
	var server bytes.Buffer
	if err := p.ClientToServer(strings.NewReader(input), &server); err != nil {
		t.Fatalf("ClientToServer: %v", err)
	}
	return &server
}

func TestClientAllowedCallIsForwarded(t *testing.T) {
	p, client, _ := newTestProxy(t)
	server := clientSend(t, p, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"getMonitor","arguments":{"id":3,"note":"<a&b>"}}}`+"\n")
	got := lines(t, server)
	if len(got) != 1 {
		t.Fatalf("server got %d messages, want 1", len(got))
	}
	params := got[0]["params"].(map[string]any)
	if params["name"] != "getMonitor" || params["arguments"].(map[string]any)["note"] != "<a&b>" {
		t.Errorf("forwarded params = %v", params)
	}
	if !strings.Contains(server.String(), "<a&b>") {
		t.Errorf("HTML characters were escaped: %s", server.String())
	}
	if client.Len() != 0 {
		t.Errorf("client got a reply for an allowed call: %s", client.String())
	}
}

func TestClientRefusedCalls(t *testing.T) {
	for name, msg := range map[string]string{
		"not listed":        `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"deleteMonitor"}}`,
		"case variant":      `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"GetMonitor"}}`,
		"repeated name key": `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"getMonitor","name":"deleteMonitor"}}`,
		"capital Method":    `{"jsonrpc":"2.0","id":7,"method":"tools/call","Method":"ping","params":{"name":"deleteMonitor"}}`,
		"capital Name":      `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"Name":"getMonitor","name":"deleteMonitor"}}`,
		"escaped name":      `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"deleteMonitor"}}`,
		"missing name":      `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{}}`,
		"name not a string": `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":["getMonitor"]}}`,
		"params not object": `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":"getMonitor"}`,
		"no params":         `{"jsonrpc":"2.0","id":7,"method":"tools/call"}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, client, logs := newTestProxy(t)
			server := clientSend(t, p, msg+"\n")
			if server.Len() != 0 {
				t.Errorf("refused call reached the server: %s", server.String())
			}
			replies := lines(t, client)
			if len(replies) != 1 {
				t.Fatalf("client got %d replies, want 1", len(replies))
			}
			e, ok := replies[0]["error"].(map[string]any)
			if !ok || e["code"].(float64) != codeInvalidParams || replies[0]["id"].(float64) != 7 {
				t.Errorf("reply = %v, want error %d for id 7", replies[0], codeInvalidParams)
			}
			if !strings.Contains(logs.String(), "refused tools/call") {
				t.Errorf("refusal not logged: %q", logs.String())
			}
		})
	}
}

func TestClientRepeatedNameLastWinsWhenAllowed(t *testing.T) {
	p, client, _ := newTestProxy(t)
	server := clientSend(t, p, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"deleteMonitor","name":"getMonitor"}}`+"\n")
	if client.Len() != 0 {
		t.Fatalf("unexpected reply: %s", client.String())
	}
	// The server must see one name only: the one the proxy checked.
	if strings.Count(server.String(), `"name"`) != 1 || !strings.Contains(server.String(), `"name":"getMonitor"`) {
		t.Errorf("forwarded %s, want a single name getMonitor", server.String())
	}
}

func TestClientRefusedNotificationIsDropped(t *testing.T) {
	p, client, _ := newTestProxy(t)
	server := clientSend(t, p, `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"deleteMonitor"}}`+"\n")
	if server.Len() != 0 || client.Len() != 0 {
		t.Errorf("server %q, client %q; want both empty", server.String(), client.String())
	}
}

func TestClientBatchAndGarbage(t *testing.T) {
	p, client, logs := newTestProxy(t)
	server := clientSend(t, p, `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"deleteMonitor"}}]`+"\n"+
		"not json\n"+
		`{"a":1} {"b":2}`+"\n"+
		"null\n"+
		"   \n")
	if server.Len() != 0 {
		t.Errorf("server got %q, want nothing", server.String())
	}
	replies := lines(t, client)
	if len(replies) != 1 || replies[0]["error"].(map[string]any)["code"].(float64) != codeInvalidRequest || replies[0]["id"] != nil {
		t.Errorf("client replies = %v, want one invalid-request error with id null", replies)
	}
	if strings.Count(logs.String(), "not a JSON object") != 3 {
		t.Errorf("logs = %q, want three dropped lines", logs.String())
	}
}

func TestClientOtherMessagesPassThrough(t *testing.T) {
	p, _, _ := newTestProxy(t)
	in := `{"jsonrpc":"2.0","id":"a","method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":5,"result":{}}` + "\n"
	got := lines(t, clientSend(t, p, in))
	if len(got) != 3 || got[0]["method"] != "initialize" || got[1]["method"] != "notifications/initialized" || got[2]["result"] == nil {
		t.Errorf("forwarded %v", got)
	}
}

const listReply = `{"jsonrpc":"2.0","id":%s,"result":{"tools":[` +
	`{"name":"getMonitor","description":"read"},` +
	`{"name":"deleteMonitor","description":"delete"},` +
	`{"name":"pauseMonitor"},` +
	`{"Name":"listMonitors","name":"createMonitor"},` +
	`{"description":"no name"},` +
	`"not an object"` +
	`]}}`

func serverSend(t *testing.T, p *Proxy, input string) {
	t.Helper()
	if err := p.ServerToClient(strings.NewReader(input)); err != nil {
		t.Fatalf("ServerToClient: %v", err)
	}
}

func toolNames(t *testing.T, reply map[string]any) []string {
	t.Helper()
	tools := reply["result"].(map[string]any)["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	return names
}

func TestToolsListIsFiltered(t *testing.T) {
	for _, id := range []string{`1`, `"req-1"`} {
		p, client, logs := newTestProxy(t)
		clientSend(t, p, `{"jsonrpc":"2.0","id":`+id+`,"method":"tools/list"}`+"\n")
		serverSend(t, p, strings.Replace(listReply, "%s", id, 1)+"\n")
		replies := lines(t, client)
		if len(replies) != 1 {
			t.Fatalf("id %s: client got %d messages", id, len(replies))
		}
		if got := strings.Join(toolNames(t, replies[0]), ","); got != "getMonitor,pauseMonitor" {
			t.Errorf("id %s: tools = %s, want getMonitor,pauseMonitor", id, got)
		}
		if !strings.Contains(logs.String(), "2 allowed, 4 hidden") || !strings.Contains(logs.String(), "not offered by the server: [listMonitors]") {
			t.Errorf("id %s: logs = %q", id, logs.String())
		}
	}
}

func TestToolsListReportLoggedOncePerChange(t *testing.T) {
	p, _, logs := newTestProxy(t)
	for i := 0; i < 3; i++ {
		clientSend(t, p, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n")
		serverSend(t, p, strings.Replace(listReply, "%s", "1", 1)+"\n")
	}
	if n := strings.Count(logs.String(), "tools/list:"); n != 1 {
		t.Errorf("report logged %d times, want 1", n)
	}
}

func TestToolsListPagedOmitsMissing(t *testing.T) {
	p, client, logs := newTestProxy(t)
	clientSend(t, p, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n")
	serverSend(t, p, `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"getMonitor"}],"nextCursor":"x"}}`+"\n")
	reply := lines(t, client)[0]
	if reply["result"].(map[string]any)["nextCursor"] != "x" {
		t.Errorf("nextCursor lost: %v", reply)
	}
	if strings.Contains(logs.String(), "not offered") {
		t.Errorf("a paged reply reported missing tools: %q", logs.String())
	}
}

func TestToolsListEdgeReplies(t *testing.T) {
	cases := map[string]struct {
		reply     string
		wantError bool
		wantTools int
	}{
		"error reply passes":   {`{"jsonrpc":"2.0","id":3,"error":{"code":-1,"message":"x"}}`, true, -1},
		"no tools key":         {`{"jsonrpc":"2.0","id":3,"result":{}}`, false, 0},
		"tools not an array":   {`{"jsonrpc":"2.0","id":3,"result":{"tools":{"name":"deleteMonitor"}}}`, true, -1},
		"result not an object": {`{"jsonrpc":"2.0","id":3,"result":[{"name":"deleteMonitor"}]}`, true, -1},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p, client, _ := newTestProxy(t)
			clientSend(t, p, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`+"\n")
			serverSend(t, p, c.reply+"\n")
			if strings.Contains(client.String(), "deleteMonitor") {
				t.Fatalf("a hidden tool reached the client: %s", client.String())
			}
			reply := lines(t, client)[0]
			if _, isErr := reply["error"]; isErr != c.wantError {
				t.Errorf("reply %v, want error=%v", reply, c.wantError)
			}
			if c.wantTools >= 0 {
				if got := len(reply["result"].(map[string]any)["tools"].([]any)); got != c.wantTools {
					t.Errorf("tools = %d, want %d", got, c.wantTools)
				}
			}
		})
	}
}

func TestServerOtherMessages(t *testing.T) {
	p, client, logs := newTestProxy(t)
	// Replies without a tools list, server requests, and notifications pass
	// unchanged.
	in := `{"jsonrpc":"2.0","id":9,"result":{"content":[]}}` + "\n" +
		`{"jsonrpc":"2.0","id":10,"method":"sampling/createMessage","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}` + "\n" +
		`[{"jsonrpc":"2.0","id":11,"result":{}}]` + "\n" +
		"npm warn something\n"
	serverSend(t, p, in)
	got := lines(t, client)
	if len(got) != 3 {
		t.Fatalf("client got %d messages, want 3: %s", len(got), client.String())
	}
	if strings.Count(logs.String(), "server line that is not a JSON object") != 2 {
		t.Errorf("logs = %q", logs.String())
	}
}

func TestUnmatchedToolsListIsFiltered(t *testing.T) {
	for name, id := range map[string]string{
		"never requested":     `9`,
		"echoed as 1.0":       `1.0`,
		"echoed in exponent":  `1e0`,
		"echoed with escapes": `"\u0061"`,
	} {
		t.Run(name, func(t *testing.T) {
			p, client, logs := newTestProxy(t)
			if name != "never requested" {
				reqID := `1`
				if strings.HasPrefix(id, `"`) {
					reqID = `"a"`
				}
				clientSend(t, p, `{"jsonrpc":"2.0","id":`+reqID+`,"method":"tools/list"}`+"\n")
			}
			serverSend(t, p, `{"jsonrpc":"2.0","id":`+id+`,"result":{"tools":[{"name":"getMonitor"},{"name":"deleteMonitor"}]}}`+"\n")
			if strings.Contains(client.String(), "deleteMonitor") || !strings.Contains(client.String(), "getMonitor") {
				t.Errorf("client got %s", client.String())
			}
			matched := strings.Contains(logs.String(), "tools/list: 1 allowed, 1 hidden")
			if wantMatched := name != "never requested"; matched != wantMatched {
				t.Errorf("matched=%v, want %v; logs %q", matched, wantMatched, logs.String())
			}
		})
	}
}

func TestIDKeyCanonical(t *testing.T) {
	same := [][]string{
		{`1`, `1.0`, `1e0`, ` 1 `, `10e-1`},
		{`"a"`, `"\u0061"`},
		{`-7`, `-7.0`},
	}
	for _, group := range same {
		for _, id := range group[1:] {
			if idKey(json.RawMessage(group[0])) != idKey(json.RawMessage(id)) {
				t.Errorf("idKey(%s) != idKey(%s)", group[0], id)
			}
		}
	}
	for _, pair := range [][2]string{{`1`, `"1"`}, {`1`, `2`}, {`1.5`, `1`}, {`null`, `"null"`}, {`1e400`, `1`}} {
		if idKey(json.RawMessage(pair[0])) == idKey(json.RawMessage(pair[1])) {
			t.Errorf("idKey(%s) == idKey(%s)", pair[0], pair[1])
		}
	}
	if idKey(json.RawMessage(`{bad`)) != "raw:{bad" || idKey(json.RawMessage(`{"a": 1}`)) != `j:{"a":1}` {
		t.Error("idKey fallback forms changed")
	}
}

func TestRefusedNameIsQuotedAndTruncated(t *testing.T) {
	p, client, logs := newTestProxy(t)
	long := strings.Repeat("x", 300)
	clientSend(t, p, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"evil\ntools/list: 99 allowed"}}`+"\n"+
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"`+long+`"}}`+"\n")
	if strings.Count(logs.String(), "\n") != 2 || strings.Contains(logs.String(), "\ntools/list") {
		t.Errorf("a name forged a log line: %q", logs.String())
	}
	if strings.Contains(logs.String(), long) || !strings.Contains(logs.String(), strings.Repeat("x", maxLoggedName)+"...") {
		t.Errorf("long name not truncated: %q", logs.String())
	}
	if strings.Contains(client.String(), long) {
		t.Errorf("long name echoed in full to the client")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestWriteErrorsStopTheLoops(t *testing.T) {
	allow, _ := allowlist.Parse(strings.NewReader("getMonitor\n"))
	p := New(allow, failWriter{}, log.New(&bytes.Buffer{}, "", 0))
	if err := p.ServerToClient(strings.NewReader(`{"jsonrpc":"2.0","method":"x"}` + "\n")); err == nil {
		t.Error("ServerToClient ignored a client write error")
	}
	if err := p.ClientToServer(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"x"}}`+"\n"), &bytes.Buffer{}); err == nil {
		t.Error("ClientToServer ignored a client write error on a refusal")
	}
	if err := p.ClientToServer(strings.NewReader(`{"jsonrpc":"2.0","method":"x"}`+"\n"), failWriter{}); err == nil {
		t.Error("ClientToServer ignored a server write error")
	}
}

func TestOversizedMessageIsAnError(t *testing.T) {
	p, _, _ := newTestProxy(t)
	big := `{"x":"` + strings.Repeat("a", MaxMessageBytes) + `"}` + "\n"
	if err := p.ServerToClient(strings.NewReader(big)); err == nil {
		t.Error("an oversized message was accepted")
	}
}

func TestServerMessagesOfAnyShapeAreFiltered(t *testing.T) {
	for name, msg := range map[string]string{
		"with a method":       `{"jsonrpc":"2.0","id":2,"method":"x","result":{"tools":[{"name":"getMonitor"},{"name":"deleteMonitor"}]}}`,
		"without an id":       `{"jsonrpc":"2.0","result":{"tools":[{"name":"getMonitor"},{"name":"deleteMonitor"}]}}`,
		"notification-shaped": `{"jsonrpc":"2.0","method":"notifications/x","result":{"tools":[{"name":"deleteMonitor"}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, client, _ := newTestProxy(t)
			serverSend(t, p, msg+"\n")
			if strings.Contains(client.String(), "deleteMonitor") {
				t.Errorf("a hidden tool reached the client: %s", client.String())
			}
		})
	}
	// An unreadable tools list in a message the client cannot answer is
	// dropped, not turned into an error reply.
	p, client, _ := newTestProxy(t)
	serverSend(t, p, `{"jsonrpc":"2.0","result":{"tools":{"name":"deleteMonitor"}}}`+"\n")
	if client.Len() != 0 {
		t.Errorf("client = %s", client.String())
	}
}
