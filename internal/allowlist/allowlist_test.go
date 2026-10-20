package allowlist

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	s, err := Parse(strings.NewReader("# read tools\n\ngetMonitor\n  listMonitors  # trailing comment\nmy.tool-name_2\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := s.Names(), []string{"getMonitor", "listMonitors", "my.tool-name_2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
	if s.Len() != 3 {
		t.Errorf("Len() = %d, want 3", s.Len())
	}
	for name, want := range map[string]bool{
		"getMonitor":    true,
		"getmonitor":    false, // exact, case sensitive
		"deleteMonitor": false,
		"":              false,
	} {
		if got := s.Allowed(name); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for name, input := range map[string]string{
		"empty":         "",
		"only comments": "# nothing\n\n",
		"space in name": "get Monitor\n",
		"slash":         "tools/call\n",
		"duplicate":     "getMonitor\ngetMonitor\n",
		"too long":      strings.Repeat("a", 129) + "\n",
		"line too long": strings.Repeat("a", 70000) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(input)); err == nil {
				t.Errorf("Parse(%s) succeeded, want an error", name)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "allow.txt")
	if err := os.WriteFile(good, []byte("getMonitor\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(good)
	if err != nil || !s.Allowed("getMonitor") {
		t.Fatalf("Load(good) = %v, %v", s.Names(), err)
	}

	bad := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(bad, []byte("not valid!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("Load(bad) error = %v, want one naming the file", err)
	}

	if _, err := Load(filepath.Join(dir, "missing.txt")); err == nil {
		t.Error("Load(missing) succeeded, want an error")
	}
}
