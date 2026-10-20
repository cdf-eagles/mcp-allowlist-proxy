package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// drainTimeout bounds how long the proxy waits, after the client closes its
// side, for requests still in flight. It exceeds the reply-header timeout,
// so a tool call that is answered at all is answered within it.
const drainTimeout = 6 * time.Minute

// HTTPClient forwards client messages to a remote MCP server over the
// Streamable HTTP transport: each message is POSTed to one endpoint, and the
// server answers with a JSON body, an event stream, or 202 Accepted. Replies
// pass through the same allowlist filter as in stdio mode.
type HTTPClient struct {
	Endpoint  string // used for requests; may carry a query
	Display   string // the endpoint without its query, for logs
	Token     string // sent as "Authorization: Bearer <token>"; never logged
	UserAgent string
	HTTP      *http.Client

	mu       sync.Mutex
	session  string // Mcp-Session-Id assigned by the server, if any
	protocol string // protocol version negotiated by initialize, if any
}

// NewHTTPClient checks the endpoint and returns a client with conservative
// connection timeouts that never follows a redirect. The endpoint must be
// https, except for a loopback host, and must carry no user information or
// fragment.
func NewHTTPClient(endpoint, token, userAgent string) (*HTTPClient, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("-url is not a valid URL")
	}
	switch {
	case u.Scheme == "https" && u.Host != "":
	case u.Scheme == "http" && isLoopback(u.Hostname()):
	default:
		return nil, errors.New("-url must be https:// (plain http:// only for a loopback host)")
	}
	if u.User != nil || u.Fragment != "" {
		return nil, errors.New("-url must not carry user information or a fragment; put the credential in -token-file")
	}
	if token == "" || strings.ContainsFunc(token, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return nil, errors.New("the bearer token is empty or contains white space or control characters")
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute, // a tool call answers when it finishes
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	display := *u
	display.RawQuery = ""
	return &HTTPClient{
		Endpoint:  u.String(),
		Display:   display.String(),
		Token:     token,
		UserAgent: userAgent,
		HTTP: &http.Client{
			Transport: transport,
			// A redirect would carry the token, the session id, and the
			// message somewhere the operator did not name; answer it as an
			// error instead.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ReadTokenFile reads a bearer token from a regular file (not a symbolic
// link) that grants no access to group or others, and trims surrounding
// white space.
func ReadTokenFile(path string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("token file %s is not a regular file", path)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("token file %s is accessible by group or others (mode %04o); chmod 600 it", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: the path is the operator's own command-line argument.
	if err != nil {
		return "", fmt.Errorf("token file: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return tok, nil
}

// ServeHTTP relays client messages read from r to the remote server until r
// ends or ctx is cancelled. initialize is sent and answered before anything
// else is read, so later messages carry the session it starts; other
// requests are sent concurrently, so a slow tool call does not hold up the
// next one; notifications and responses are sent in order. When r ends, the
// proxy waits for requests in flight (at most drainTimeout) and ends the
// server session. When ctx is cancelled, it returns at once.
func (p *Proxy) ServeHTTP(ctx context.Context, r io.Reader, c *HTTPClient) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	done := make(chan error, 1)
	go func() {
		done <- scanLines(r, func(line []byte) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			out, reply := p.fromClient(line)
			if reply != nil {
				if err := p.writeClient(reply); err != nil {
					return err
				}
			}
			if out == nil {
				return nil
			}
			msg, _ := decodeObject(out)
			id, hasID := msg["id"]
			method, _ := stringField(msg, "method")
			if hasID && method != "" && method != "initialize" {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := p.post(ctx, c, out, id, method); err != nil {
						p.Log.Printf("%s: %v", method, err)
					}
				}()
				return nil
			}
			var reqID json.RawMessage
			if hasID && method != "" {
				reqID = id
			}
			if err := p.post(ctx, c, out, reqID, method); err != nil {
				p.Log.Printf("sending %s: %v", describe(method), err)
			}
			return nil
		})
	}()

	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		c.endSession()
		return nil
	}
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	drained := make(chan struct{})
	go func() { wg.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(drainTimeout):
		p.Log.Printf("gave up waiting for requests in flight after %s", drainTimeout)
	case <-ctx.Done():
	}
	c.endSession()
	return err
}

func describe(method string) string {
	if method == "" {
		return "a response"
	}
	return method
}

// post sends one message and delivers every message in the reply to the
// client. For a request (id non-nil), the client always gets an answer: the
// server's response, or a JSON-RPC error when the transport fails, the
// server answers with an HTTP error or a redirect, or the reply holds no
// response to it.
func (p *Proxy) post(ctx context.Context, c *HTTPClient, body []byte, id json.RawMessage, method string) error {
	fail := func(err error) error {
		if id != nil {
			if werr := p.writeClient(errorReply(id, -32603, "mcp-allowlist-proxy: "+err.Error())); werr != nil {
				return werr
			}
		}
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fail(errors.New("could not build the request"))
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fail(fmt.Errorf("server unreachable: %w", unwrapURLError(err)))
	}
	defer func() { _ = resp.Body.Close() }()

	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		c.mu.Lock()
		c.session = s
		c.mu.Unlock()
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fail(fmt.Errorf("server answered HTTP %d", resp.StatusCode))
	}
	want := ""
	if id != nil {
		want = idKey(id)
	}
	answered := false
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch {
	case resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent:
	case mediaType == "text/event-stream":
		answered, err = p.readEvents(resp.Body, c, method, want)
		if err != nil && !answered {
			return fail(err)
		}
	case mediaType == "application/json" || mediaType == "":
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, MaxMessageBytes+1))
		switch {
		case rerr != nil:
			return fail(errors.New("reading the reply failed"))
		case len(b) > MaxMessageBytes:
			return fail(errors.New("reply larger than the message limit"))
		}
		if len(bytes.TrimSpace(b)) > 0 {
			answered, err = p.deliver(b, c, method, want)
			if err != nil {
				return err
			}
		}
	default:
		return fail(fmt.Errorf("unexpected reply type %q", mediaType))
	}
	if id != nil && !answered {
		return fail(errors.New("the server sent no response to this request"))
	}
	return err
}

// unwrapURLError drops the URL that *url.Error carries, so an error message
// never repeats the endpoint's query.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// deliver filters one reply body (an object, or an array of objects) and
// writes what passes to the client. It reports whether a response to the
// request id want (an idKey, or "" for none) was among the messages.
func (p *Proxy) deliver(b []byte, c *HTTPClient, method, want string) (bool, error) {
	if t := bytes.TrimSpace(b); len(t) > 0 && t[0] == '[' {
		var items []json.RawMessage
		if err := json.Unmarshal(t, &items); err != nil {
			p.Log.Printf("dropped a reply that is not valid JSON")
			return false, nil //nolint:nilerr // dropped and logged, as stdio mode drops a non-JSON line
		}
		answered := false
		for _, it := range items {
			ok, err := p.deliver(it, c, method, want)
			if err != nil {
				return answered, err
			}
			answered = answered || ok
		}
		return answered, nil
	}
	answered := false
	if msg, ok := decodeObject(b); ok && want != "" {
		_, isRequest := msg["method"]
		if id, hasID := msg["id"]; hasID && !isRequest && idKey(id) == want {
			answered = true
		}
	}
	if method == "initialize" {
		c.noteProtocol(b)
	}
	out := p.fromServer(b)
	if out == nil {
		return false, nil
	}
	return answered, p.writeClient(out)
}

// readEvents delivers the data of every message event in an event stream,
// and reports whether a response to want was among them.
func (p *Proxy) readEvents(r io.Reader, c *HTTPClient, method, want string) (bool, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), MaxMessageBytes)
	var data []string
	size := 0
	event := ""
	answered := false
	flush := func() error {
		defer func() { data, size, event = nil, 0, "" }()
		payload := strings.Join(data, "\n")
		if strings.TrimSpace(payload) == "" || (event != "" && event != "message") {
			return nil
		}
		ok, err := p.deliver([]byte(payload), c, method, want)
		answered = answered || ok
		return err
	}
	for sc.Scan() {
		line := strings.TrimPrefix(sc.Text(), "\ufeff")
		switch {
		case line == "":
			if err := flush(); err != nil {
				return answered, err
			}
		case strings.HasPrefix(line, ":"):
			// comment or keep-alive
		case strings.HasPrefix(line, "data:"):
			d := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			size += len(d) + 1
			if size > MaxMessageBytes {
				return answered, errors.New("event larger than the message limit")
			}
			data = append(data, d)
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	if err := sc.Err(); err != nil {
		return answered, fmt.Errorf("reading the event stream: %w", err)
	}
	return answered, flush()
}

func (c *HTTPClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	if c.protocol != "" {
		req.Header.Set("Mcp-Protocol-Version", c.protocol)
	}
}

// noteProtocol records the protocol version from an initialize result, which
// later requests must carry in the Mcp-Protocol-Version header.
func (c *HTTPClient) noteProtocol(b []byte) {
	msg, ok := decodeObject(b)
	if !ok {
		return
	}
	result, ok := decodeObject(msg["result"])
	if !ok {
		return
	}
	if v, ok := stringField(result, "protocolVersion"); ok {
		c.mu.Lock()
		c.protocol = v
		c.mu.Unlock()
	}
}

// endSession asks the server to end a session it assigned, with a context of
// its own so it is still sent after a signal. A server without sessions
// never assigns one, and the request is not sent.
func (c *HTTPClient) endSession() {
	c.mu.Lock()
	session := c.session
	c.mu.Unlock()
	if session == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.Endpoint, http.NoBody)
	if err != nil {
		return
	}
	c.setHeaders(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}
