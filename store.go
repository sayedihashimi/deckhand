package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// The session list is saved so that Claude sessions can be resumed
// (`claude --resume`) after deckhand restarts.

func storePath() string { return filepath.Join(dataDir(), "sessions.json") }

func loadSessions() []*Session {
	data, err := os.ReadFile(storePath())
	if err != nil {
		return nil
	}
	var list []*Session
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	out := list[:0]
	for _, s := range list {
		if s == nil || !validID(s.ID) {
			continue
		}
		s.status = StatusExited
		s.detail = "⏎ restart"
		if a, ok := agentByName(s.Agent); ok && a.CanResume() && s.Started {
			s.detail = "⏎ resume"
		}
		out = append(out, s)
	}
	return out
}

func saveSessions(list []*Session) error {
	for _, s := range list {
		s.mu.Lock()
	}
	data, err := json.MarshalIndent(list, "", "  ")
	for _, s := range list {
		s.mu.Unlock()
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return err
	}
	tmp := storePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, storePath())
}

func removeSessionFiles(id string) {
	if validID(id) {
		_ = os.RemoveAll(sessionDir(id))
	}
}
