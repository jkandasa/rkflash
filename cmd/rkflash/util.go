package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jkandasa/rkflash/internal/logx"
	"github.com/jkandasa/rkflash/internal/rockusb"
	"github.com/jkandasa/rkflash/internal/usbfs"
)

const sectorSize = rockusb.SectorSize

// parseSectors parses a sector number, or a byte size with a K/M/G suffix.
func parseSectors(s string) (uint64, error) {
	var mult uint64
	switch {
	case strings.HasSuffix(s, "K"), strings.HasSuffix(s, "k"):
		mult = 1 << 10
	case strings.HasSuffix(s, "M"), strings.HasSuffix(s, "m"):
		mult = 1 << 20
	case strings.HasSuffix(s, "G"), strings.HasSuffix(s, "g"):
		mult = 1 << 30
	}
	if mult != 0 {
		v, err := strconv.ParseUint(s[:len(s)-1], 0, 64)
		if err != nil {
			return 0, fmt.Errorf("bad size %q", s)
		}
		b := v * mult
		if b%sectorSize != 0 {
			return 0, fmt.Errorf("%s is not a multiple of 512 bytes", s)
		}
		return b / sectorSize, nil
	}
	v, err := strconv.ParseUint(s, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("bad number %q", s)
	}
	return v, nil
}

// parseUint32 parses a number with an optional 0x prefix.
func parseUint32(s string) (uint32, error) {
	v, err := strconv.ParseUint(s, 0, 32)
	if err != nil {
		return 0, fmt.Errorf("bad number %q", s)
	}
	return uint32(v), nil
}

// humanBytes formats a byte count.
func humanBytes(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}

// isTerminal reports whether f is a character device (a terminal).
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// progress draws a single-line progress bar on stderr.
type progress struct {
	label   string
	total   uint64
	done    uint64
	start   time.Time
	last    time.Time
	enabled bool
}

func newProgress(g *globals, label string, total uint64) *progress {
	now := time.Now()
	return &progress{label: label, total: total, start: now, last: now,
		enabled: !g.noProgress && logx.Level() < 2 && isTerminal(os.Stderr)}
}

func (p *progress) add(n uint64) {
	p.done += n
	if p.enabled && (time.Since(p.last) >= 200*time.Millisecond || p.done >= p.total) {
		p.last = time.Now()
		p.draw()
	}
}

func (p *progress) rate() float64 {
	if el := time.Since(p.start).Seconds(); el > 0 {
		return float64(p.done) / el
	}
	return 0
}

func (p *progress) draw() {
	pct := 100.0
	if p.total > 0 {
		pct = float64(p.done) * 100 / float64(p.total)
	}
	const width = 30
	fill := int(pct / 100 * width)
	bar := strings.Repeat("#", fill) + strings.Repeat(".", width-fill)
	eta := ""
	if r := p.rate(); r > 0 && p.done < p.total {
		eta = " ETA " + time.Duration(float64(p.total-p.done)/r*float64(time.Second)).Round(time.Second).String()
	}
	fmt.Fprintf(os.Stderr, "\r\033[K%-6s [%s] %5.1f%%  %s / %s  %s/s%s",
		p.label, bar, pct, humanBytes(p.done), humanBytes(p.total), humanBytes(uint64(p.rate())), eta)
}

// finish ends the bar and prints a summary line.
func (p *progress) finish(w io.Writer) {
	if p.enabled {
		p.draw()
		fmt.Fprintln(os.Stderr)
	}
	el := time.Since(p.start)
	fmt.Fprintf(w, "%s: %s in %s (%s/s)\n", p.label, humanBytes(p.done), el.Round(time.Millisecond), humanBytes(uint64(p.rate())))
}

// confirm asks a yes/no question unless -y is set.
func confirm(g *globals, format string, a ...any) error {
	if g.yes {
		return nil
	}
	if !isTerminal(os.Stdin) {
		return fmt.Errorf("refusing to continue without confirmation; pass -y")
	}
	fmt.Fprintf(os.Stderr, format+" [y/N]: ", a...)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return exitError{1}
}

// Device access. Tests replace these hooks with the simulator.
var (
	listDevices = usbfs.List
	openLoader  = openLoaderUSB
	openMaskrom = openMaskromUSB
)

func selectDevice(g *globals) (usbfs.DeviceInfo, error) {
	devs, err := listDevices()
	if err != nil {
		return usbfs.DeviceInfo{}, err
	}
	if len(devs) == 0 {
		return usbfs.DeviceInfo{}, fmt.Errorf("no Rockchip device found (connect USB and enter Maskrom or Loader mode)")
	}
	if g.device != "" {
		var bus, dev int
		if _, err := fmt.Sscanf(g.device, "%d:%d", &bus, &dev); err != nil {
			return usbfs.DeviceInfo{}, fmt.Errorf("bad -d %q, want bus:dev", g.device)
		}
		for _, d := range devs {
			if d.Bus == bus && d.Dev == dev {
				return d, nil
			}
		}
		return usbfs.DeviceInfo{}, fmt.Errorf("device %s not found", g.device)
	}
	if len(devs) > 1 {
		return usbfs.DeviceInfo{}, fmt.Errorf("%d devices found, select one with -d bus:dev", len(devs))
	}
	return devs[0], nil
}

func openLoaderUSB(g *globals) (*rockusb.Client, func(), error) {
	info, err := selectDevice(g)
	if err != nil {
		return nil, nil, err
	}
	if m := info.Mode(); m != usbfs.ModeLoader {
		return nil, nil, fmt.Errorf("device is in %s mode; this command needs Loader mode (run 'db <loader.bin>' first)", m)
	}
	d, err := usbfs.Open(info)
	if err != nil {
		return nil, nil, err
	}
	logx.Infof("using %s (bus %d dev %d, %04x:%04x, Loader)", info.Port, info.Bus, info.Dev, usbfs.RockchipVID, info.PID)
	c := rockusb.NewClient(d)
	c.Timeout, c.EraseTimeout = g.timeout, g.eraseTimeout
	return c, func() { d.Close() }, nil
}

func openMaskromUSB(g *globals) (rockusb.Control, func(), error) {
	info, err := selectDevice(g)
	if err != nil {
		return nil, nil, err
	}
	if m := info.Mode(); m != usbfs.ModeMaskrom {
		return nil, nil, fmt.Errorf("device is in %s mode; 'db' works only in Maskrom mode", m)
	}
	d, err := usbfs.Open(info)
	if err != nil {
		return nil, nil, err
	}
	logx.Infof("using %s (bus %d dev %d, %04x:%04x, Maskrom)", info.Port, info.Bus, info.Dev, usbfs.RockchipVID, info.PID)
	return d, func() { d.Close() }, nil
}

// waitForMode polls until a device in the wanted mode appears.
func waitForMode(g *globals, want string, timeout time.Duration) (usbfs.DeviceInfo, error) {
	deadline := time.Now().Add(timeout)
	for {
		devs, err := listDevices()
		if err != nil {
			return usbfs.DeviceInfo{}, err
		}
		for _, d := range devs {
			if want == "any" || strings.EqualFold(string(d.Mode()), want) {
				return d, nil
			}
		}
		if time.Now().After(deadline) {
			return usbfs.DeviceInfo{}, fmt.Errorf("no device in %s mode after %s", want, timeout)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
