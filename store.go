package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

// The session list is saved so that Claude sessions can be resumed
// (`claude --resume`) after deckhand restarts. Only the main instance
// reads and writes it (instance.go).
//
// Fields and entries this build doesn't know are kept and written back
// unchanged, so an older build doesn't drop a newer one's data: each
// session keeps its unknown fields (Session.extra), and entries that
// aren't sessions this build can load are kept as they are (m.storeExtra)
// and written after the sessions.

func storePath() string { return filepath.Join(dataDir(), "sessions.json") }

// extraFields is the fields of a JSON object that a struct doesn't have.
type extraFields map[string]json.RawMessage

// jsonKeys is the JSON field names of a struct type, lower-cased (decoding
// matches names ignoring case).
func jsonKeys(t reflect.Type) map[string]bool {
	keys := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		keys[strings.ToLower(name)] = true
	}
	return keys
}

// objectFields is the fields of the JSON object data, or nil.
func objectFields(data []byte) map[string]json.RawMessage {
	var all map[string]json.RawMessage
	if json.Unmarshal(data, &all) != nil {
		return nil
	}
	return all
}

// unknownFields is the fields of the JSON object data that aren't in known
// (from jsonKeys).
func unknownFields(data []byte, known map[string]bool) extraFields {
	var out extraFields
	for k, v := range objectFields(data) {
		if known[strings.ToLower(k)] {
			continue
		}
		if out == nil {
			out = extraFields{}
		}
		out[k] = v
	}
	return out
}

// withExtra adds the fields in extra to the end of the JSON object data, in
// name order, leaving out any it already has.
func withExtra(data []byte, extra extraFields) ([]byte, error) {
	if len(extra) == 0 {
		return data, nil
	}
	have := objectFields(data)
	names := make([]string, 0, len(extra))
	for k := range extra {
		if _, ok := have[k]; !ok {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	var b bytes.Buffer
	b.Write(bytes.TrimSuffix(bytes.TrimSpace(data), []byte("}")))
	first := len(have) == 0
	for _, k := range names {
		if !first {
			b.WriteByte(',')
		}
		first = false
		key, _ := json.Marshal(k)
		b.Write(key)
		b.WriteByte(':')
		b.Write(extra[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// sessionJSON is Session without its methods, so the (un)marshalers below
// don't call themselves.
type sessionJSON Session

var sessionKeys = jsonKeys(reflect.TypeOf(sessionJSON{}))

func (s *Session) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, (*sessionJSON)(s)); err != nil {
		return err
	}
	s.extra = unknownFields(data, sessionKeys)
	return nil
}

// MarshalJSON writes the session with the fields it was loaded with that
// this build doesn't know. The caller holds s.mu.
func (s *Session) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal((*sessionJSON)(s))
	if err != nil {
		return nil, err
	}
	return withExtra(data, s.extra)
}

// sessionsFile is sessions.json: the sessions this build loads, and the
// entries it doesn't (kept as they are).
type sessionsFile struct {
	sessions []*Session
	extra    []json.RawMessage
}

func readSessionsFile() sessionsFile {
	var f sessionsFile
	data, err := os.ReadFile(storePath())
	if err != nil {
		return f
	}
	var raw []json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return f
	}
	for _, r := range raw {
		var s *Session
		if json.Unmarshal(r, &s) != nil || s == nil || !validID(s.ID) {
			f.extra = append(f.extra, r)
			continue
		}
		s.status = StatusExited
		s.detail = "⏎ restart"
		if a, ok := agentByName(s.Agent); ok && a.CanResume() && s.Started {
			s.detail = "⏎ resume"
		}
		f.sessions = append(f.sessions, s)
	}
	return f
}

func loadSessions() []*Session { return readSessionsFile().sessions }

// saveSessions writes sessions.json. A temporary instance doesn't.
func (m *model) saveSessions() error {
	if m.temp {
		return nil
	}
	return writeSessionsFile(m.sessions, m.storeExtra)
}

func writeSessionsFile(all []*Session, extra []json.RawMessage) error {
	// A session whose worktree is still being made isn't saved: after a
	// restart it would start without one. Nor is an agent test that hasn't
	// been kept.
	list := make([]any, 0, len(all)+len(extra))
	var locked []*Session
	for _, s := range all {
		if !s.creating.Load() && !s.testing.Load() {
			list = append(list, s)
			locked = append(locked, s)
		}
	}
	for _, r := range extra {
		list = append(list, r)
	}
	for _, s := range locked {
		s.mu.Lock()
	}
	data, err := json.MarshalIndent(list, "", "  ")
	for _, s := range locked {
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
