// Command mcp-allowlist-proxy sits between a Model Context Protocol (MCP)
// client on its standard input and output and an MCP server, and lets through
// only the tools named in an allowlist. The server is either a child process
// (stdio) or a remote endpoint (Streamable HTTP).
//
// Usage:
//
//	mcp-allowlist-proxy -allow FILE -- COMMAND [ARG...]
//	mcp-allowlist-proxy -allow FILE -url URL -token-file FILE
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/cdf-eagles/mcp-allowlist-proxy/internal/allowlist"
	"github.com/cdf-eagles/mcp-allowlist-proxy/internal/proxy"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is main without the process exit, so tests can drive it.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// The logger and the server's stderr copy share one writer.
	stderr = &lockedWriter{w: stderr}
	logger := log.New(stderr, "mcp-allowlist-proxy: ", 0)

	fs := flag.NewFlagSet("mcp-allowlist-proxy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	allowPath := fs.String("allow", "", "file listing the allowed tool names, one per line (required)")
	endpoint := fs.String("url", "", "remote MCP endpoint (Streamable HTTP); https:// except for a loopback host")
	tokenPath := fs.String("token-file", "", "file holding the remote endpoint's bearer token, no access for group or others (required with -url)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: mcp-allowlist-proxy -allow FILE -- COMMAND [ARG...]")
		_, _ = fmt.Fprintln(stderr, "       mcp-allowlist-proxy -allow FILE -url URL -token-file FILE")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stdout, version)
		return 0
	}
	child := fs.Args()
	remote := *endpoint != "" || *tokenPath != ""
	if problem := usageProblem(*allowPath, *endpoint, *tokenPath, len(child) > 0); problem != "" {
		logger.Print(problem)
		fs.Usage()
		return 2
	}

	allow, err := allowlist.Load(*allowPath)
	if err != nil {
		logger.Print(err)
		return 1
	}
	logger.Printf("version %s, %d tools allowed by %s", version, allow.Len(), *allowPath)

	if remote {
		return runRemote(*endpoint, *tokenPath, allow, stdin, stdout, logger)
	}

	// The server's lifetime is the proxy's: no context cancels it; signals
	// are forwarded below and the proxy exits when the server does.
	cmd := exec.CommandContext(context.Background(), child[0], child[1:]...)
	cmd.Stderr = stderr
	toServer, err := cmd.StdinPipe()
	if err != nil {
		logger.Printf("server stdin: %v", err)
		return 1
	}
	fromServer, err := cmd.StdoutPipe()
	if err != nil {
		logger.Printf("server stdout: %v", err)
		return 1
	}
	if err := cmd.Start(); err != nil {
		logger.Printf("starting %s: %v", child[0], err)
		return 1
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	go func() {
		for s := range signals {
			_ = cmd.Process.Signal(s)
		}
	}()

	p := proxy.New(allow, stdout, logger)

	// Client to server: when the client closes stdin, close the server's
	// stdin so it shuts down as it would without the proxy.
	go func() {
		if err := p.ClientToServer(stdin, toServer); err != nil {
			logger.Printf("client to server: %v", err)
		}
		_ = toServer.Close()
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := p.ServerToClient(fromServer); err != nil {
			logger.Printf("server to client: %v", err)
			_ = cmd.Process.Kill()
		}
	}()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if code := exit.ExitCode(); code >= 0 {
				return code
			}
			logger.Printf("server ended by %v", exit)
			return 1
		}
		logger.Printf("waiting for server: %v", err)
		return 1
	}
	return 0
}

// lockedWriter serialises writes from several goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b) //nolint:wrapcheck // a transparent writer adds no context.
}

// runRemote relays to a Streamable HTTP endpoint until the client closes its
// side, or until SIGINT, SIGTERM, or SIGHUP cancels requests in flight.
func runRemote(endpoint, tokenPath string, allow allowlist.Set, stdin io.Reader, stdout io.Writer, logger *log.Logger) int {
	token, err := proxy.ReadTokenFile(tokenPath)
	if err != nil {
		logger.Print(err)
		return 1
	}
	client, err := proxy.NewHTTPClient(endpoint, token, "mcp-allowlist-proxy/"+version)
	if err != nil {
		logger.Print(err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	logger.Printf("relaying to %s", client.Display)
	if err := proxy.New(allow, stdout, logger).ServeHTTP(ctx, stdin, client); err != nil {
		logger.Printf("client to server: %v", err)
		return 1
	}
	return 0
}

// usageProblem names what is wrong with a combination of flags, or returns "".
func usageProblem(allowPath, endpoint, tokenPath string, hasCommand bool) string {
	remote := endpoint != "" || tokenPath != ""
	switch {
	case allowPath == "":
		return "-allow is required"
	case remote && hasCommand:
		return "give either -url with -token-file or a COMMAND, not both"
	case endpoint != "" && tokenPath == "":
		return "-url needs -token-file"
	case tokenPath != "" && endpoint == "":
		return "-token-file needs -url"
	case !remote && !hasCommand:
		return "give a COMMAND after --, or -url with -token-file"
	}
	return ""
}
