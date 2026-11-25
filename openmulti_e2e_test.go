//go:build e2e

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestE2EOpenMultiFolder: a session with a start folder and a code repo
// beside it. o shows the picker with All folders first; a folder added with
// /add-dir in the transcript shows up the next time o is pressed. All
// folders in "VS Code" (recordapp named code.exe) opens the workspace file
// deckhand wrote to its data dir, and t's All folders runs "wt.exe"
// (recordapp too) once, with a tab per folder. Nothing real is launched.
func TestE2EOpenMultiFolder(t *testing.T) {
	exe := buildDeckhand(t)
	bin := t.TempDir()
	code, wt := filepath.Join(bin, "code.exe"), filepath.Join(bin, "wt.exe")
	for _, app := range []string{code, wt} {
		if out, err := exec.Command("go", "build", "-o", app, "./testdata/recordapp").CombinedOutput(); err != nil {
			t.Fatalf("build recordapp: %v\n%s", err, out)
		}
	}
	src := t.TempDir()
	notes, api, docs := filepath.Join(src, "notes"), filepath.Join(src, "api"), filepath.Join(src, "docs")
	for _, d := range []string{notes, api, docs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tr := filepath.Join(t.TempDir(), "11111111-2222-3333-4444-555555555555.jsonl")
	_ = os.WriteFile(tr, []byte(`{"type":"user","message":{"role":"user","content":"hello"}}`+"\n"), 0o600)

	home := t.TempDir()
	saved := fmt.Sprintf(`[{"id":"multi-0001","name":"proj","agent":"pwsh","dir":%q,"code_repo":%q,"transcript_path":%q,"created":"2026-01-01T00:00:00Z"}]`, notes, api, tr)
	if err := os.WriteFile(filepath.Join(home, "sessions.json"), []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	logDir := t.TempDir()
	editorLog, termLog := filepath.Join(logDir, "editor.log"), filepath.Join(logDir, "terminal.log")
	cfg := filepath.Join(t.TempDir(), "config.toml")
	_ = os.WriteFile(cfg, []byte(fmt.Sprintf("[open]\neditor = '\"%s\" \"%s\" \"{dir}\"'\nterminal = '\"%s\" \"%s\" -w 0 nt -d \"{dir}\"'\n",
		code, editorLog, wt, termLog)), 0o644)

	env := append(envWith(home, ""), "DECKHAND_CONFIG="+cfg)
	h := startHarness(t, exe, env, t.TempDir())
	h.waitFor("proj", 10*time.Second)
	if strings.Contains(h.screen(), "Config error") {
		t.Fatalf("config error; screen:\n%s", h.screen())
	}

	// Two folders: the picker, on All folders.
	h.send("o")
	h.waitFor(`Open "proj" in the editor`, 5*time.Second)
	h.waitFor("› All folders", 3*time.Second)
	h.waitFor("code repo", 3*time.Second)
	if strings.Contains(h.screen(), "added (/add-dir)") {
		t.Fatalf("nothing was added yet; screen:\n%s", h.screen())
	}
	h.send("\x1b")
	h.waitGone("All folders", 3*time.Second)

	// /add-dir in the transcript: listed the next time.
	entry, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user",
		"content": "<command-name>/add-dir</command-name>\n            <command-message>add-dir</command-message>\n            <command-args>" + docs + "</command-args>"}})
	f, _ := os.OpenFile(tr, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.Write(append(entry, '\n'))
	f.Close()
	h.send("o")
	h.waitFor("added (/add-dir)", 5*time.Second)
	t.Logf("picker:\n%s", h.screen())
	h.send("\r")
	h.waitGone("All folders", 3*time.Second)

	recs := waitRecords(t, h, editorLog, 1)
	ws := filepath.Join(home, "sessions", "multi-0001", "proj.code-workspace")
	if !reflect.DeepEqual(recs[0].Args, []string{ws}) {
		t.Fatalf("All folders launched %+v, want the workspace %s", recs[0], ws)
	}
	data, err := os.ReadFile(ws)
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Folders []struct{ Name, Path string } `json:"folders"`
	}
	if err := json.Unmarshal(data, &w); err != nil || len(w.Folders) != 3 ||
		w.Folders[0].Path != notes || w.Folders[1].Path != api || w.Folders[2].Path != docs || w.Folders[1].Name != "api (code repo)" {
		t.Fatalf("workspace %v:\n%s", err, data)
	}

	// One row: only that folder.
	h.send("o")
	h.waitFor("› All folders", 5*time.Second)
	h.send("\x1b[B\x1b[B")
	h.waitFor("› code repo", 3*time.Second)
	h.send("\r")
	recs = waitRecords(t, h, editorLog, 2)
	if !reflect.DeepEqual(recs[1].Args, []string{api}) || recs[1].Cwd != api {
		t.Fatalf("the code repo row launched %+v", recs[1])
	}

	// t: the same picker; All folders is one wt.exe with three tabs.
	h.send("t")
	h.waitFor(`Open "proj" in a terminal`, 5*time.Second)
	h.send("\r")
	recs = waitRecords(t, h, termLog, 1)
	want := []string{"-w", "0", "nt", "-d", notes, ";", "nt", "-d", api, ";", "nt", "-d", docs}
	if !reflect.DeepEqual(recs[0].Args, want) || recs[0].Cwd != notes {
		t.Fatalf("t All launched %+v, want %q", recs[0], want)
	}
	if strings.Contains(h.screen(), "Couldn't open") {
		t.Fatalf("unexpected error; screen:\n%s", h.screen())
	}

	h.send("q")
	h.waitExit(10 * time.Second)
}

type openRecord struct {
	Cwd  string   `json:"cwd"`
	Args []string `json:"args"`
}

// waitRecords waits for recordapp to have logged n runs.
func waitRecords(t *testing.T, h *harness, log string, n int) []openRecord {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(log)
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if strings.HasSuffix(string(data), "\n") && len(lines) >= n {
			var out []openRecord
			for _, l := range lines {
				var r openRecord
				if err := json.Unmarshal([]byte(l), &r); err != nil {
					t.Fatalf("log line %q: %v", l, err)
				}
				out = append(out, r)
			}
			return out
		}
		time.Sleep(100 * time.Millisecond)
	}
	data, _ := os.ReadFile(log)
	t.Fatalf("%s has fewer than %d runs: %q; screen:\n%s", log, n, data, h.screen())
	return nil
}
