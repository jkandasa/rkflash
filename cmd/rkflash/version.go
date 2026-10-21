package main

import (
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
)

// Set at build time, e.g.:
//
//	go build -ldflags "-X main.version=v1.0.0 -X main.commit=abc123 -X main.buildDate=2026-01-01T00:00:00Z"
//
// "go install" builds fall back to the VCS data that Go embeds.
var (
	version   = "dev"
	commit    = ""
	buildDate = ""
)

// buildInfo fills commit and build time from embedded VCS data when unset.
func buildInfo() (ver, rev, date string, dirty bool) {
	ver, rev, date = version, commit, buildDate
	if bi, ok := debug.ReadBuildInfo(); ok {
		if ver == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			ver = bi.Main.Version
		}
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if rev == "" {
					rev = s.Value
				}
			case "vcs.time":
				if date == "" {
					date = s.Value
				}
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	return
}

func printVersion(w io.Writer) {
	ver, rev, date, dirty := buildInfo()
	if rev == "" {
		rev = "unknown"
	} else if dirty {
		rev += " (modified)"
	}
	if date == "" {
		date = "unknown"
	}
	fmt.Fprintf(w, "rkflash %s\n", ver)
	fmt.Fprintf(w, "  commit:     %s\n", rev)
	fmt.Fprintf(w, "  build time: %s\n", date)
	fmt.Fprintf(w, "  go version: %s\n", runtime.Version())
	fmt.Fprintf(w, "  platform:   %s/%s\n", runtime.GOOS, runtime.GOARCH)
}
