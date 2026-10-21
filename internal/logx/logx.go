// Package logx is a small leveled logger for diagnostics on stderr.
//
// Level 0 prints nothing. Level 1 (-v) prints operations and timings.
// Level 2 (-vv) adds protocol traces. Level 3 (-vvv) adds data dumps.
package logx

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

var (
	mu    sync.Mutex
	out   io.Writer = os.Stderr
	level int
	start = time.Now()
	// clearLine erases a progress bar on a terminal before a log line.
	clearLine = func() string {
		if st, err := os.Stderr.Stat(); err == nil && st.Mode()&os.ModeCharDevice != 0 {
			return "\r\033[K"
		}
		return ""
	}()
)

// SetLevel sets the verbosity level.
func SetLevel(l int) { level = l }

// Level returns the verbosity level.
func Level() int { return level }

// SetOutput redirects log output (used by tests).
func SetOutput(w io.Writer) { mu.Lock(); out, clearLine = w, ""; mu.Unlock() }

func logf(l int, tag, format string, a ...any) {
	if level < l {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	fmt.Fprintf(out, "%s[%8.3f] %-5s %s\n", clearLine, time.Since(start).Seconds(), tag, fmt.Sprintf(format, a...))
}

// Infof logs at level 1.
func Infof(format string, a ...any) { logf(1, "info", format, a...) }

// Debugf logs at level 2.
func Debugf(format string, a ...any) { logf(2, "debug", format, a...) }

// Tracef logs at level 3.
func Tracef(format string, a ...any) { logf(3, "trace", format, a...) }

// Dump logs up to max bytes of b as hex at level 3.
func Dump(prefix string, b []byte, max int) {
	if level < 3 {
		return
	}
	n := len(b)
	if n > max {
		n = max
	}
	for off := 0; off < n; off += 16 {
		end := min(off+16, n)
		logf(3, "trace", "%s %04x: % x", prefix, off, b[off:end])
	}
	if len(b) > n {
		logf(3, "trace", "%s ... (%d bytes total)", prefix, len(b))
	}
}
