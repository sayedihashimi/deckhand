package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// A Claude session's name follows Claude's own title for the conversation
// ([list] follow_claude_title, on by default). Claude writes two kinds of
// title to the transcript:
//
//   - {"type":"custom-title","customTitle":"…"}: /rename, and also the name
//     Claude was started with (a wrapper passing --name), written again on
//     each resume.
//   - {"type":"ai-title","aiTitle":"…"}: Claude's automatic title.
//
// The name starts as the folder's. The latest ai-title replaces it while
// the session hasn't been renamed. A /rename renames it, and so does r; the
// latest of the two wins. A custom-title counts as a /rename only when it
// differs from the previous one in the transcript, so the launch name and
// its copy on resume don't count. The first custom-title counts only after
// the conversation has begun (a user or assistant line): before that it's
// the launch name.
//
// The name's source is saved with it (Session.NameSource), and so is how
// many renames of which conversation were seen (Session.Title), so a
// restart, which reads the transcript from the start again, doesn't apply
// an older title again.

// Where a session's name came from.
const (
	nameFolder = "folder" // the folder (or worktree branch) it started in
	nameAI     = "ai"     // Claude's automatic title
	nameClaude = "claude" // /rename in Claude
	nameUser   = "user"   // r in deckhand, or a fork's "(fork)" name
)

// titleSeen is the part of a transcript's titles already applied to the
// session's name.
type titleSeen struct {
	Conv    string `json:"conv,omitempty"`    // the conversation (transcript) it's from
	Renames int    `json:"renames,omitempty"` // how many /renames it had
	Rename  string `json:"rename,omitempty"`  // the latest of them
}

// transcriptTitles is what a transcript says about its title so far.
type transcriptTitles struct {
	conv    string
	renames int    // custom-titles that count as /rename
	rename  string // the latest of them
	ai      string // the latest ai-title
}

// titleScan reads a transcript's titles as it grows: each read adds the
// lines written since the last one, like transcriptCost.
type titleScan struct {
	path    string
	offset  int64
	started bool   // a user or assistant line has been read
	custom  string // the latest custom-title, counted or not
	hasAny  bool   // a custom-title has been read
	titles  transcriptTitles
}

// titleLine is the part of a transcript line that titles are read from.
type titleLine struct {
	Type        string `json:"type"`
	CustomTitle string `json:"customTitle"`
	AITitle     string `json:"aiTitle"`
}

var (
	customTitleMark = []byte(`"custom-title"`)
	aiTitleMark     = []byte(`"ai-title"`)
	userMark        = []byte(`"user"`)
	assistantMark   = []byte(`"assistant"`)
)

// read adds what's new in the transcript at path; a different path, or a
// shorter file, starts over.
func (ts *titleScan) read(path string) (transcriptTitles, error) {
	if path != ts.path {
		*ts = titleScan{path: path}
	}
	f, err := os.Open(path)
	if err != nil {
		return ts.titles, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ts.titles, err
	}
	if fi.Size() < ts.offset {
		*ts = titleScan{path: path}
	}
	ts.titles.conv = transcriptConv(path)
	if ts.titles.conv == "" {
		ts.titles.conv = path
	}
	if fi.Size() == ts.offset {
		return ts.titles, nil
	}
	if _, err := f.Seek(ts.offset, io.SeekStart); err != nil {
		return ts.titles, err
	}
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break // a line without its newline is still being written
		}
		ts.offset += int64(len(line))
		ts.line(line)
	}
	return ts.titles, nil
}

// line takes one transcript line.
func (ts *titleScan) line(line []byte) {
	custom, ai := bytes.Contains(line, customTitleMark), bytes.Contains(line, aiTitleMark)
	msg := !ts.started && (bytes.Contains(line, userMark) || bytes.Contains(line, assistantMark))
	if !custom && !ai && !msg {
		return
	}
	var e titleLine
	if json.Unmarshal(line, &e) != nil {
		return
	}
	switch e.Type {
	case "user", "assistant":
		ts.started = true
	case "custom-title":
		t := oneLine(e.CustomTitle)
		if t == "" {
			return
		}
		counts := t != ts.custom && (ts.hasAny || ts.started)
		ts.custom, ts.hasAny = t, true
		if counts {
			ts.titles.renames++
			ts.titles.rename = t
		}
	case "ai-title":
		if t := oneLine(e.AITitle); t != "" {
			ts.titles.ai = t
		}
	}
}

// parseTitles reads a whole transcript's titles (for tests).
func parseTitles(data string) transcriptTitles {
	var ts titleScan
	for _, l := range strings.SplitAfter(data, "\n") {
		if strings.HasSuffix(l, "\n") {
			ts.line([]byte(l))
		}
	}
	return ts.titles
}

// nameSourceLocked is where the name came from. Sessions saved before the
// source was are "folder" when the name is still the folder's (or the
// worktree's branch), else "user".
func (s *Session) nameSourceLocked() string {
	if s.NameSource != "" {
		return s.NameSource
	}
	if s.Name == sessionName(s.Dir) || (s.Branch != "" && s.Name == s.Branch) {
		return nameFolder
	}
	return nameUser
}

// followsTitle reports whether the session's name follows Claude's title:
// a Claude agent (wrappers too) and the setting on.
func (s *Session) followsTitle() bool {
	if !listOpts.followTitle || s.testing.Load() {
		return false
	}
	a, ok := s.agent()
	return ok && a.Claude
}

// pollTitle reads the transcript's titles. It runs on the poll goroutine;
// the model applies them (applyTitle).
func (s *Session) pollTitle(tr string) {
	if tr == "" || !s.followsTitle() {
		return
	}
	t, err := s.trTitle.read(tr)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.info.titles, s.info.titlesOK = t, true
	s.mu.Unlock()
}

// applyTitle renames the session after the transcript's title, when it
// should. It runs on the model's goroutine (names change only there) and
// reports whether anything to save changed.
func (s *Session) applyTitle() bool {
	if !s.followsTitle() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.info.titlesOK {
		return false
	}
	t := s.info.titles
	name, src := s.Name, s.nameSourceLocked()
	if newName, newSrc, ok := titleName(s.Title, src, t); ok {
		name, src = newName, newSrc
	}
	seen := &titleSeen{Conv: t.conv, Renames: t.renames, Rename: t.rename}
	changed := name != s.Name || src != s.NameSource || s.Title == nil || *s.Title != *seen
	s.Name, s.NameSource, s.Title = name, src, seen
	return changed
}

// titleName is the name the transcript's titles give a session whose name
// came from src, given what was already seen; ok is false when the name
// stays as it is.
//
// A /rename is new when the conversation has more of them than were seen.
// A new conversation (a fork, or a fresh start) begins from none, but its
// latest /rename has to differ from the last one seen, since a fork's
// transcript starts as a copy of the original's. Before anything was seen
// at all, a name set with r is kept: the session was saved before titles
// were followed, or renamed before its first read.
func titleName(seen *titleSeen, src string, t transcriptTitles) (name, newSrc string, ok bool) {
	var was titleSeen
	if seen != nil {
		was = *seen
	}
	before := was.Renames
	if was.Conv != t.conv {
		before = 0
	}
	newRename := t.renames > before && t.rename != "" &&
		(was.Conv == t.conv || t.rename != was.Rename) &&
		(seen != nil || src != nameUser)
	switch {
	case newRename:
		return t.rename, nameClaude, true
	case (src == nameFolder || src == nameAI) && t.ai != "":
		return t.ai, nameAI, true
	}
	return "", "", false
}
