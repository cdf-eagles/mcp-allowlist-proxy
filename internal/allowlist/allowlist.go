// Package allowlist reads the list of Model Context Protocol (MCP) tool names
// that the proxy lets through.
package allowlist

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

// toolName matches the characters the MCP specification allows in a tool
// name (letters, digits, underscore, hyphen, and dot; 1 to 128 characters).
var toolName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// Set is an immutable set of allowed tool names.
type Set struct {
	names map[string]struct{}
}

// Allowed reports whether name is in the set. The comparison is exact and
// case sensitive, as MCP servers match tool names.
func (s Set) Allowed(name string) bool {
	_, ok := s.names[name]
	return ok
}

// Len returns the number of names in the set.
func (s Set) Len() int {
	return len(s.names)
}

// Names returns the names in the set, sorted.
func (s Set) Names() []string {
	out := make([]string, 0, len(s.names))
	for n := range s.names {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Parse reads one tool name per line. Blank lines and lines whose first
// non-blank character is '#' are ignored, as is anything after a '#' on a
// name line. An invalid name, a repeated name, or an empty list is an error,
// so a mistake in the file stops the proxy instead of widening or silently
// narrowing what it admits.
func Parse(r io.Reader) (Set, error) {
	names := map[string]struct{}{}
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if i := strings.IndexByte(text, '#'); i >= 0 {
			text = text[:i]
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if !toolName.MatchString(text) {
			return Set{}, fmt.Errorf("line %d: %q is not a valid tool name", line, text)
		}
		if _, dup := names[text]; dup {
			return Set{}, fmt.Errorf("line %d: %q is listed twice", line, text)
		}
		names[text] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return Set{}, fmt.Errorf("reading allowlist: %w", err)
	}
	if len(names) == 0 {
		return Set{}, errors.New("allowlist names no tools")
	}
	return Set{names: names}, nil
}

// Load reads the allowlist file at path.
func Load(path string) (Set, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the path is the operator's own command-line argument.
	if err != nil {
		return Set{}, fmt.Errorf("opening allowlist: %w", err)
	}
	defer func() { _ = f.Close() }()
	s, err := Parse(f)
	if err != nil {
		return Set{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}
