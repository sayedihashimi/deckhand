//go:build e2e

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2EPathAliases runs deckhand with a [[paths.alias]] for a temporary
// src folder: the list, the header, the new-session dialog's recent folder
// and a typed alias all show "src\..." and never the real prefix. The
// session isn't started.
func TestE2EPathAliases(t *testing.T) {
	exe := buildDeckhand(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	api := filepath.Join(src, "api")
	for _, d := range []string{api, filepath.Join(src, "notes")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	data, _ := json.Marshal([]map[string]any{{"id": "alias-api", "name": "api", "agent": "pwsh", "dir": api, "created": time.Now()}})
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, "config.toml")
	_ = os.WriteFile(cfg, []byte("[[paths.alias]]\nprefix = '"+src+"'\nname = 'src'\n"), 0o644)
	h := startHarness(t, exe, append(envWith(home, ""), "DECKHAND_CONFIG="+cfg), root)

	noPrefix := func(where string) {
		t.Helper()
		if low := strings.ToLower(h.screen()); strings.Contains(low, strings.ToLower(src)) {
			t.Fatalf("%s shows the aliased prefix:\n%s", where, h.screen())
		}
	}
	h.waitFor(`src\api`, 10*time.Second) // the list's folder line and the header
	noPrefix("the list")

	h.send("n")
	h.waitFor("Start in", 5*time.Second)
	h.waitFor(`src\api  recent`, 3*time.Second)
	noPrefix("the dialog")

	// An alias with a \ is a path: it lists that folder, and tab fills in
	// the folder the same way.
	h.send(`src\no`)
	h.waitFor(`notes\`, 3*time.Second)
	h.send("\t")
	h.waitFor(`Start in  src\notes  ✓`, 3*time.Second)
	noPrefix("the dialog after tab")
	h.send("\x1b")
	h.waitGone("Start in", 3*time.Second)
	h.send("q")
	h.waitExit(10 * time.Second)
}
