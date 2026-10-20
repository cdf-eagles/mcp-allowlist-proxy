// Package proxy relays Model Context Protocol (MCP) stdio traffic between a
// client and a server process and enforces a tool allowlist on it.
//
// The stdio transport carries one JSON-RPC message per line. The proxy:
//
//   - removes every tool that is not on the allowlist from every server
//     reply that carries a tools list (the replies to tools/list, matched or
//     not), so the client never sees it;
//   - answers a tools/call for a tool that is not on the allowlist with a
//     JSON-RPC error itself, without forwarding it to the server;
//   - drops anything it cannot read as a single JSON object (batches and
//     non-JSON lines), in both directions, so nothing passes unexamined.
//
// Messages are decoded by exact key, never through Go's case-insensitive
// struct matching, and client messages are re-encoded from the decoded form,
// so the server sees exactly what the proxy checked.
package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"strconv"
	"sync"

	"github.com/cdf-eagles/mcp-allowlist-proxy/internal/allowlist"
)

// MaxMessageBytes is the largest single message the proxy accepts.
const MaxMessageBytes = 64 << 20

// codeInvalidParams is the JSON-RPC code MCP servers use for an unknown tool.
const codeInvalidParams = -32602

// codeInvalidRequest is the JSON-RPC code for a request the proxy refuses to
// relay because of its form.
const codeInvalidRequest = -32600

// maxLoggedName is the longest client-supplied tool name the proxy logs.
const maxLoggedName = 128

// Proxy relays one client-server session.
type Proxy struct {
	Allow allowlist.Set
	Log   *log.Logger

	clientOut io.Writer
	outMu     sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]struct{} // ids of tools/list requests in flight

	reportMu sync.Mutex
	reported string // last tools/list summary logged, to log changes only
}

// New returns a Proxy that writes to the client through clientOut.
func New(allow allowlist.Set, clientOut io.Writer, logger *log.Logger) *Proxy {
	return &Proxy{
		Allow:     allow,
		Log:       logger,
		clientOut: clientOut,
		pending:   map[string]struct{}{},
	}
}

type object = map[string]json.RawMessage

// ClientToServer reads client messages from r until EOF and writes the ones
// it lets through to w. It does not close w.
func (p *Proxy) ClientToServer(r io.Reader, w io.Writer) error {
	return scanLines(r, func(line []byte) error {
		out, reply := p.fromClient(line)
		if reply != nil {
			if err := p.writeClient(reply); err != nil {
				return err
			}
		}
		if out == nil {
			return nil
		}
		return writeLine(w, out)
	})
}

// ServerToClient reads server messages from r until EOF and writes them to
// the client, filtered.
func (p *Proxy) ServerToClient(r io.Reader) error {
	return scanLines(r, func(line []byte) error {
		out := p.fromServer(line)
		if out == nil {
			return nil
		}
		return p.writeClient(out)
	})
}

// fromClient returns the message to forward to the server (nil to forward
// nothing) and a reply to send the client directly (nil for none).
func (p *Proxy) fromClient(line []byte) (forward, reply []byte) {
	msg, ok := decodeObject(line)
	if !ok {
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 && trimmed[0] == '[' {
			p.Log.Printf("refused a JSON-RPC batch from the client")
			return nil, errorReply(json.RawMessage("null"), codeInvalidRequest, "mcp-allowlist-proxy does not relay JSON-RPC batches")
		}
		p.Log.Printf("dropped a client line that is not a JSON object")
		return nil, nil
	}
	id, hasID := msg["id"]
	method, _ := stringField(msg, "method")

	switch method {
	case "tools/call":
		name, ok := toolNameOf(msg)
		if !ok || !p.Allow.Allowed(name) {
			if !ok {
				name = "(unreadable)"
			}
			shown := name
			if len(shown) > maxLoggedName {
				shown = shown[:maxLoggedName] + "..."
			}
			p.Log.Printf("refused tools/call %q: not on the allowlist", shown)
			if !hasID {
				return nil, nil
			}
			return nil, errorReply(id, codeInvalidParams, fmt.Sprintf("Tool %q is not permitted by the allowlist of mcp-allowlist-proxy", shown))
		}
	case "tools/list":
		if hasID {
			p.pendingMu.Lock()
			p.pending[idKey(id)] = struct{}{}
			p.pendingMu.Unlock()
		}
	}

	out, err := encode(msg)
	if err != nil {
		p.Log.Printf("dropped a client message that could not be re-encoded: %v", err)
		return nil, nil
	}
	return out, nil
}

// fromServer returns the message to forward to the client, or nil. Every
// message with a result that carries a tools array is filtered, whether or
// not its id matches a tools/list request the proxy saw (and whatever other
// keys it has), so no shape of server message can carry a hidden tool past
// the proxy.
func (p *Proxy) fromServer(line []byte) []byte {
	msg, ok := decodeObject(line)
	if !ok {
		p.Log.Printf("dropped a server line that is not a JSON object")
		return nil
	}
	id, hasID := msg["id"]
	_, isRequest := msg["method"]
	isList := false
	if hasID && !isRequest {
		key := idKey(id)
		p.pendingMu.Lock()
		_, isList = p.pending[key]
		delete(p.pending, key)
		p.pendingMu.Unlock()
	}
	raw, hasResult := msg["result"]
	if !hasResult {
		return line // requests, notifications, and error replies carry no tools
	}
	if !isList {
		result, isObject := decodeObject(raw)
		if !isObject {
			return line
		}
		if _, hasTools := result["tools"]; !hasTools {
			return line
		}
	}
	filtered, err := p.filterToolsResult(raw, isList)
	if err != nil {
		p.Log.Printf("dropped a tools/list reply that could not be filtered: %v", err)
		if !hasID || isRequest {
			return nil
		}
		return errorReply(id, codeInvalidRequest, "mcp-allowlist-proxy could not read the server's tools/list reply")
	}
	msg["result"] = filtered
	out, err := encode(msg)
	if err != nil {
		p.Log.Printf("dropped a tools/list reply that could not be re-encoded: %v", err)
		return errorReply(id, codeInvalidRequest, "mcp-allowlist-proxy could not re-encode the server's tools/list reply")
	}
	return out
}

// filterToolsResult keeps only allowed tools in a tools/list result. It logs
// the full outcome only for replies matched to a tools/list request.
func (p *Proxy) filterToolsResult(raw json.RawMessage, matched bool) (json.RawMessage, error) {
	result, ok := decodeObject(raw)
	if !ok {
		return nil, errors.New("result is not an object")
	}
	var tools []json.RawMessage
	if rawTools, has := result["tools"]; has {
		if err := json.Unmarshal(rawTools, &tools); err != nil {
			return nil, fmt.Errorf("tools is not an array: %w", err)
		}
	}
	kept := make([]json.RawMessage, 0, len(tools))
	seen := map[string]struct{}{}
	var dropped []string
	for _, t := range tools {
		tool, ok := decodeObject(t)
		if !ok {
			dropped = append(dropped, "(unreadable)")
			continue
		}
		name, ok := stringField(tool, "name")
		if ok && p.Allow.Allowed(name) {
			kept = append(kept, t)
			seen[name] = struct{}{}
			continue
		}
		if !ok {
			name = "(unreadable)"
		}
		dropped = append(dropped, name)
	}
	var missing []string
	for _, n := range p.Allow.Names() {
		if _, ok := seen[n]; !ok {
			missing = append(missing, n)
		}
	}
	if matched {
		p.report(len(kept), dropped, missing, result["nextCursor"] != nil)
	} else {
		p.Log.Printf("filtered a tools list in a reply to an unmatched id: %d allowed, %d hidden", len(kept), len(dropped))
	}

	keptRaw, err := json.Marshal(kept)
	if err != nil {
		return nil, fmt.Errorf("encoding tools: %w", err)
	}
	result["tools"] = keptRaw
	return encode(result)
}

// report logs the outcome of a tools/list filter when it differs from the
// last one logged. The names logged are tool names only, never arguments.
func (p *Proxy) report(kept int, dropped, missing []string, paged bool) {
	s := fmt.Sprintf("tools/list: %d allowed, %d hidden %v", kept, len(dropped), dropped)
	if len(missing) > 0 && !paged {
		s += fmt.Sprintf("; allowlisted but not offered by the server: %v", missing)
	}
	p.reportMu.Lock()
	defer p.reportMu.Unlock()
	if s == p.reported {
		return
	}
	p.reported = s
	p.Log.Print(s)
}

func (p *Proxy) writeClient(b []byte) error {
	p.outMu.Lock()
	defer p.outMu.Unlock()
	return writeLine(p.clientOut, b)
}

func writeLine(w io.Writer, b []byte) error {
	buf := make([]byte, 0, len(b)+1)
	buf = append(buf, b...)
	buf = append(buf, '\n')
	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("writing message: %w", err)
	}
	return nil
}

func scanLines(r io.Reader, handle func([]byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), MaxMessageBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if err := handle(line); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("reading messages: %w", err)
	}
	return nil
}

// decodeObject decodes b as a single JSON object with exact keys. A repeated
// key keeps its last value, as JavaScript's JSON.parse does.
func decodeObject(b []byte) (object, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	var o object
	if err := dec.Decode(&o); err != nil || o == nil {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false // trailing data after the object
	}
	return o, true
}

func stringField(o object, key string) (string, bool) {
	raw, ok := o[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// toolNameOf returns params.name of a tools/call and re-encodes params from
// its decoded form, so a repeated "name" key cannot reach the server.
func toolNameOf(msg object) (string, bool) {
	params, ok := decodeObject(msg["params"])
	if !ok {
		return "", false
	}
	name, ok := stringField(params, "name")
	if !ok {
		return "", false
	}
	b, err := encode(params)
	if err != nil {
		return "", false
	}
	msg["params"] = b
	return name, true
}

func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encoding JSON: %w", err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// idKey normalises a JSON-RPC id so the server's echo of it matches: a
// string by its decoded value, a number by its value (1, 1.0 and 1e0 are one
// id), anything else by its compact form.
func idKey(id json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(id))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "raw:" + string(id)
	}
	switch x := v.(type) {
	case string:
		return "s:" + x
	case json.Number:
		if i, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
			return "n:" + strconv.FormatInt(i, 10)
		}
		f, err := strconv.ParseFloat(x.String(), 64)
		if err == nil && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			return "n:" + strconv.FormatInt(int64(f), 10)
		}
		if err == nil {
			return "n:" + strconv.FormatFloat(f, 'g', -1, 64)
		}
		return "n:" + x.String()
	default:
		var buf bytes.Buffer
		if err := json.Compact(&buf, id); err != nil {
			return "raw:" + string(id)
		}
		return "j:" + buf.String()
	}
}

func errorReply(id json.RawMessage, code int, message string) []byte {
	b, err := encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
	if err != nil {
		// Encoding a map of plain values cannot fail; keep a fixed reply anyway.
		return []byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"mcp-allowlist-proxy internal error"}}`)
	}
	return b
}
