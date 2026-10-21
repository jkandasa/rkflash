// Command rkflash flashes and inspects Rockchip devices over USB in Maskrom
// or Loader mode, and builds or inspects Rockchip boot images.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jkandasa/rkflash/internal/logx"
)

// version is set at build time: go build -ldflags "-X main.version=1.2.3".
var version = "dev"

// globals holds options accepted by every command.
type globals struct {
	device       string
	verbose      int
	timeout      time.Duration
	eraseTimeout time.Duration
	chunk        uint
	yes          bool
	noProgress   bool
}

// verboseFlag counts repeated -v flags.
type verboseFlag struct{ n *int }

func (v verboseFlag) String() string {
	if v.n == nil {
		return "0"
	}
	return fmt.Sprint(*v.n)
}
func (v verboseFlag) IsBoolFlag() bool { return true }
func (v verboseFlag) Set(s string) error {
	switch s {
	case "true":
		*v.n++
	case "false":
	default:
		var n int
		if _, err := fmt.Sscan(s, &n); err != nil {
			return fmt.Errorf("verbosity must be a number")
		}
		*v.n = n
	}
	return nil
}

func (g *globals) register(fs *flag.FlagSet) {
	fs.StringVar(&g.device, "d", g.device, "select device `bus:dev` (see 'list'); needed with more than one device")
	fs.Var(verboseFlag{&g.verbose}, "v", "verbose output; repeat for more (-v, -vv, -vvv)")
	fs.DurationVar(&g.timeout, "timeout", g.timeout, "USB command timeout")
	fs.DurationVar(&g.eraseTimeout, "erase-timeout", g.eraseTimeout, "timeout of one erase command")
	fs.UintVar(&g.chunk, "chunk", g.chunk, "sectors per read/write command (1-65535)")
	fs.BoolVar(&g.yes, "y", g.yes, "do not ask for confirmation")
	fs.BoolVar(&g.noProgress, "no-progress", g.noProgress, "disable the progress bar")
}

// command is one CLI subcommand.
type command struct {
	name    string
	aliases []string
	group   string
	args    string // usage of positional arguments
	summary string
	help    string // optional long help
	flags   func(fs *flag.FlagSet)
	nargs   [2]int // min, max positional args (-1 = unlimited)
	run     func(c *cmdCtx) error
}

// cmdCtx is passed to a running command.
type cmdCtx struct {
	ctx  context.Context
	g    *globals
	args []string
	out  io.Writer
}

var commands []*command

func register(c *command) { commands = append(commands, c) }

func findCommand(name string) *command {
	for _, c := range commands {
		if c.name == name {
			return c
		}
		for _, a := range c.aliases {
			if a == name {
				return c
			}
		}
	}
	return nil
}

var groupOrder = []struct{ key, title string }{
	{"Device", "Device commands (USB)"}, {"Storage", "Storage commands (USB, Loader mode)"},
	{"Partitions", "Partition commands"}, {"Images", "Image commands (offline)"}, {"Other", "Other commands"},
}

func defaultGlobals() *globals {
	return &globals{timeout: 20 * time.Second, eraseTimeout: 5 * time.Minute, chunk: 128}
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, "rkflash %s - Rockchip USB flash and image tool\n\n", version)
	fmt.Fprintf(w, "Usage: rkflash [global flags] <command> [flags] [args]\n")
	for _, grp := range groupOrder {
		fmt.Fprintf(w, "\n%s:\n", grp.title)
		var list []*command
		for _, c := range commands {
			if c.group == grp.key {
				list = append(list, c)
			}
		}
		for _, c := range list {
			name := c.name
			if len(c.aliases) > 0 {
				name += " (" + strings.Join(c.aliases, ", ") + ")"
			}
			fmt.Fprintf(w, "  %-28s %s\n", name, c.summary)
		}
	}
	fmt.Fprintf(w, "\nGlobal flags (allowed before or after the command):\n")
	fs := flag.NewFlagSet("rkflash", flag.ContinueOnError)
	defaultGlobals().register(fs)
	fs.SetOutput(w)
	fs.PrintDefaults()
	fmt.Fprintf(w, `
Numbers are 512-byte sectors. Use 0x for hex. A K, M, or G suffix gives a
byte size, e.g. "16M" = 32768 sectors.

Run 'rkflash help <command>' for details.
`)
}

func commandUsage(w io.Writer, c *command, fs *flag.FlagSet) {
	fmt.Fprintf(w, "Usage: rkflash %s [flags] %s\n\n%s\n", c.name, c.args, c.summary)
	if len(c.aliases) > 0 {
		fmt.Fprintf(w, "Aliases: %s\n", strings.Join(c.aliases, ", "))
	}
	if c.help != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(c.help))
	}
	fmt.Fprintf(w, "\nFlags:\n")
	fs.SetOutput(w)
	fs.PrintDefaults()
}

// expandVerbose turns -vv and -vvv into repeated -v flags.
func expandVerbose(args []string) []string {
	var out []string
	for _, a := range args {
		if len(a) > 2 && strings.HasPrefix(a, "-v") && strings.Trim(a[1:], "v") == "" {
			for range a[1:] {
				out = append(out, "-v")
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// parseInterleaved parses flags mixed with positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// exitError carries an exit status.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

func run(args []string, stdout, stderr io.Writer) int {
	g := defaultGlobals()
	args = expandVerbose(args)

	root := flag.NewFlagSet("rkflash", flag.ContinueOnError)
	root.SetOutput(stderr)
	g.register(root)
	showVersion := root.Bool("version", false, "print the version")
	root.Usage = func() { printUsage(stderr) }
	if err := root.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "rkflash %s\n", version)
		return 0
	}
	rest := root.Args()
	if len(rest) == 0 {
		printUsage(stderr)
		return 2
	}
	name := rest[0]
	if name == "help" {
		if len(rest) > 1 {
			if c := findCommand(rest[1]); c != nil {
				fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
				g.register(fs)
				if c.flags != nil {
					c.flags(fs)
				}
				commandUsage(stdout, c, fs)
				return 0
			}
			fmt.Fprintf(stderr, "rkflash: unknown command %q\n", rest[1])
			return 2
		}
		printUsage(stdout)
		return 0
	}
	c := findCommand(name)
	if c == nil {
		fmt.Fprintf(stderr, "rkflash: unknown command %q (run 'rkflash help')\n", name)
		return 2
	}
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	g.register(fs)
	if c.flags != nil {
		c.flags(fs)
	}
	fs.Usage = func() { commandUsage(stderr, c, fs) }
	pos, err := parseInterleaved(fs, rest[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if len(pos) < c.nargs[0] || (c.nargs[1] >= 0 && len(pos) > c.nargs[1]) {
		fmt.Fprintf(stderr, "Usage: rkflash %s [flags] %s\n", c.name, c.args)
		return 2
	}
	if g.chunk == 0 || g.chunk > 0xffff {
		fmt.Fprintln(stderr, "rkflash: -chunk must be 1..65535")
		return 2
	}
	logx.SetLevel(g.verbose)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	start := time.Now()
	logx.Infof("rkflash %s: %s %s", version, c.name, strings.Join(pos, " "))
	err = c.run(&cmdCtx{ctx: ctx, g: g, args: pos, out: stdout})
	logx.Infof("%s finished in %s", c.name, time.Since(start).Round(time.Millisecond))
	if err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			return ee.code
		}
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr, "rkflash: interrupted")
			return 130
		}
		fmt.Fprintf(stderr, "rkflash: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
