package main

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Set DECKHAND_DEBUG to a file path to log key handling and the bytes
// forwarded to each agent.
var (
	debugOnce sync.Once
	debugFile *os.File
	debugMu   sync.Mutex
)

func debugf(format string, args ...any) {
	debugOnce.Do(func() {
		if p := os.Getenv("DECKHAND_DEBUG"); p != "" {
			debugFile, _ = os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		}
	})
	if debugFile == nil {
		return
	}
	debugMu.Lock()
	defer debugMu.Unlock()
	fmt.Fprintf(debugFile, time.Now().Format("15:04:05.000")+" "+format+"\n", args...)
}

// copyInput forwards encoded keys and terminal replies from the emulator to
// the agent.
func copyInput(dst io.Writer, src io.Reader, id string) {
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			debugf("to %s: %q", id[:8], buf[:n])
			if _, werr := dst.Write(buf[:n]); werr != nil {
				debugf("write to %s failed: %v", id[:8], werr)
				return
			}
		}
		if err != nil {
			return
		}
	}
}
