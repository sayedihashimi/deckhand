package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Main and temporary instances (#58), like Notepad's windows. The main
// instance owns the saved state: it loads sessions.json and groups.json
// and saves changes. Any deckhand started while a main one is running for
// the same data folder is temporary: it starts with an empty list and the
// new-session dialog open, never reads or writes sessions.json or
// groups.json, and deletes its sessions' folders when it exits. deckhand
// --temp is always temporary.
//
// Which one is main is decided by a lock the main instance holds for its
// whole life: a named mutex on Windows (instance_windows.go), flock on
// deckhand.lock elsewhere (instance_other.go). The OS releases it when the
// process ends or crashes, so there's no stale lock to clean up.
//
// state.json holds the format of the saved files. A build that finds a
// newer format than it knows runs as temporary rather than overwrite them.

// stateFormat is the format of sessions.json and groups.json this build
// writes. Raise it when an older build can no longer read them safely.
const stateFormat = 1

func statePath() string { return filepath.Join(dataDir(), "state.json") }

// mainLockName is the main instance's mutex: one per data folder, so two
// DECKHAND_HOME folders each get their own main instance.
func mainLockName() string {
	dir := dataDir()
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(dir))))
	return `Local\deckhand-main-` + hex.EncodeToString(sum[:])[:16]
}

// savedFormat is the format in state.json; 0 when there's none.
func savedFormat() int {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return 0
	}
	var st struct {
		Format int `json:"format"`
	}
	if json.Unmarshal(data, &st) != nil {
		return 0
	}
	return st.Format
}

// writeStateFormat records this build's format in state.json, unless the
// file already says so.
func writeStateFormat() error {
	if savedFormat() == stateFormat {
		return nil
	}
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(map[string]int{"format": stateFormat}, "", "  ")
	tmp := statePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath())
}

const newerFormatNotice = "Sessions were saved by a newer deckhand; this window won't save."

// chooseInstance decides whether this process is the main instance. With
// --temp (forceTemp) or a newer saved format it doesn't try for the lock.
// notice is a message to show at startup, if any.
func chooseInstance(forceTemp bool) (temp bool, notice string) {
	if forceTemp {
		return true, ""
	}
	if savedFormat() > stateFormat {
		return true, newerFormatNotice
	}
	if !acquireMainLock() {
		return true, ""
	}
	_ = writeStateFormat()
	return false, ""
}

// cleanupTempSessions deletes the folders of a temporary instance's
// sessions once their processes have ended. It waits up to timeout for the
// processes, since a running agent can still hold files open, and tries a
// few times.
func cleanupTempSessions(sessions []*Session, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for _, s := range sessions {
		for s.Running() && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
	}
	for _, s := range sessions {
		if !validID(s.ID) {
			continue
		}
		dir := sessionDir(s.ID)
		for {
			err := os.RemoveAll(dir)
			if err == nil || errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// withFileLock runs fn while holding an exclusive lock on path, so that
// instances sharing a data folder take turns at a read-modify-write.
func withFileLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fn() // can't lock: still save, as before locking existed
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return fn()
	}
	defer unlockFile(f)
	return fn()
}

// What a temporary instance says when asked for something it can't do.
const (
	tempGroupsNote = "Groups are saved only in the main deckhand."
	tempConfigNote = "temporary window: config can be edited in the main deckhand"
	tempHelpNote   = "Temporary window: its sessions aren't saved, and are gone when it closes."
)
