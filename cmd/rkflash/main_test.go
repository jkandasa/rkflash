package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkandasa/rkflash/internal/logx"
	"github.com/jkandasa/rkflash/internal/rkimage"
	"github.com/jkandasa/rkflash/internal/rockusb"
	"github.com/jkandasa/rkflash/internal/sim"
	"github.com/jkandasa/rkflash/internal/usbfs"
)

const diskSectors = 0x40000 // 128 MiB

// useSim routes device access to a simulated loader.
func useSim(t *testing.T, mode usbfs.Mode) *sim.Device {
	t.Helper()
	dev := sim.New(diskSectors)
	bcd := uint16(0x0201)
	if mode == usbfs.ModeMaskrom {
		bcd = 0x0200
	}
	info := usbfs.DeviceInfo{Port: "sim", Bus: 1, Dev: 2, PID: 0x310c, BcdUSB: bcd, EpIn: 0x81, EpOut: 0x02}
	oldList, oldLoader, oldMaskrom := listDevices, openLoader, openMaskrom
	listDevices = func() ([]usbfs.DeviceInfo, error) { return []usbfs.DeviceInfo{info}, nil }
	openLoader = func(*globals) (*rockusb.Client, func(), error) { return rockusb.NewClient(dev), func() {}, nil }
	openMaskrom = func(*globals) (rockusb.Control, func(), error) {
		info.BcdUSB = 0x0201 // after download the device shows up as a loader
		return dev, func() {}, nil
	}
	t.Cleanup(func() { listDevices, openLoader, openMaskrom = oldList, oldLoader, oldMaskrom; logx.SetLevel(0) })
	return dev
}

func rk(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(append([]string{"-no-progress"}, args...), &out, &errb)
	return out.String(), errb.String(), code
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, errs, code := rk(t, args...)
	if code != 0 {
		t.Fatalf("rkflash %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, out, errs)
	}
	return out
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/512)
	}
	return b
}

func TestVersion(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"-version"}, {"version"}} {
		out := mustRun(t, args...)
		for _, k := range []string{"rkflash ", "commit:", "build time:", "go version: go", "platform:"} {
			if !strings.Contains(out, k) {
				t.Errorf("%v: output lacks %q:\n%s", args, k, out)
			}
		}
	}
}

func TestHelpAndUsage(t *testing.T) {
	out := mustRun(t, "help")
	for _, s := range []string{"download-boot (db)", "write-part (wlx)", "Image commands", "-vv"} {
		if !strings.Contains(out, s) {
			t.Errorf("help lacks %q", s)
		}
	}
	if _, _, code := rk(t, "nonsense"); code != 2 {
		t.Errorf("unknown command exit %d", code)
	}
	if _, _, code := rk(t, "read", "0"); code != 2 {
		t.Errorf("missing args exit %d", code)
	}
	if out := mustRun(t, "help", "wl"); !strings.Contains(out, "-verify") {
		t.Errorf("command help: %s", out)
	}
}

func TestListAndInfo(t *testing.T) {
	useSim(t, usbfs.ModeLoader)
	out := mustRun(t, "ld")
	if !strings.Contains(out, "1:2") || !strings.Contains(out, "Loader") || !strings.Contains(out, "RK3126/RK3128") {
		t.Errorf("list: %s", out)
	}
	out = mustRun(t, "info", "-capability")
	for _, s := range []string{`"EMMC"`, `"312A"`, "262144 sectors", "Direct LBA:", "enabled"} {
		if !strings.Contains(out, s) {
			t.Errorf("info lacks %q:\n%s", s, out)
		}
	}
}

func TestWriteReadVerify(t *testing.T) {
	useSim(t, usbfs.ModeLoader)
	data := pattern(300*512 + 77) // not sector aligned, spans several chunks
	in := writeTemp(t, "in.img", data)
	out := mustRun(t, "wl", "-verify", "0x100", in)
	if !strings.Contains(out, "Verify OK") {
		t.Errorf("write output: %s", out)
	}
	back := filepath.Join(t.TempDir(), "back.img")
	mustRun(t, "rl", "0x100", "301", back)
	got, _ := os.ReadFile(back)
	if !bytes.Equal(got[:len(data)], data) || !bytes.Equal(got[len(data):], make([]byte, len(got)-len(data))) {
		t.Fatal("read back differs")
	}
	if _, errs, code := rk(t, "rl", "0x3ffff", "2", back); code == 0 || !strings.Contains(errs, "beyond") {
		t.Errorf("out of range: %d %s", code, errs)
	}
}

func TestEraseAndZero(t *testing.T) {
	dev := useSim(t, usbfs.ModeLoader)
	copy(dev.Disk, bytes.Repeat([]byte{0xff}, 64<<20))
	mustRun(t, "erase", "0", "16M")
	mustRun(t, "zero", "16M", "1M")
	if !bytes.Equal(dev.Disk[:17<<20], make([]byte, 17<<20)) || dev.Disk[17<<20] != 0xff {
		t.Fatal("erase/zero range wrong")
	}
	if _, _, code := rk(t, "ef"); code == 0 {
		t.Error("erase-flash ran without confirmation")
	}
	mustRun(t, "ef", "-y")
	if !bytes.Equal(dev.Disk[:64<<20], make([]byte, 64<<20)) {
		t.Fatal("erase-flash left data")
	}
}

func TestGPTPartitionsByName(t *testing.T) {
	useSim(t, usbfs.ModeLoader)
	mustRun(t, "gpt", "-y", "../../testdata/parameter.txt")
	out := mustRun(t, "ppt")
	if !strings.Contains(out, "Partition table: gpt") || !strings.Contains(out, "rootfs") {
		t.Fatalf("ppt: %s", out)
	}
	data := pattern(5000)
	mustRun(t, "wlx", "-verify", "trust", writeTemp(t, "trust.img", data))
	back := filepath.Join(t.TempDir(), "trust.out")
	mustRun(t, "rlx", "trust", back)
	got, _ := os.ReadFile(back)
	if len(got) != 0x2000*512 || !bytes.Equal(got[:len(data)], data) {
		t.Fatalf("partition read back: %d bytes", len(got))
	}
	big := writeTemp(t, "big.img", make([]byte, 0x2001*512))
	if _, errs, code := rk(t, "wlx", "trust", big); code == 0 || !strings.Contains(errs, "partition has") {
		t.Errorf("oversized write: %d %s", code, errs)
	}
	if _, errs, code := rk(t, "rlx", "nope", back); code == 0 || !strings.Contains(errs, "not found") {
		t.Errorf("unknown partition: %d %s", code, errs)
	}
}

func TestParameterBlock(t *testing.T) {
	dev := useSim(t, usbfs.ModeLoader)
	mustRun(t, "prm", "-y", "../../testdata/parameter.txt")
	for i := 0; i < rkimage.ParmCopies; i++ {
		if string(dev.Disk[i*rkimage.ParmStride*512:][:4]) != "PARM" {
			t.Fatalf("copy %d missing", i)
		}
	}
	out := mustRun(t, "ppt")
	if !strings.Contains(out, "Partition table: parameter") || !strings.Contains(out, "grow") {
		t.Fatalf("ppt: %s", out)
	}
	back := filepath.Join(t.TempDir(), "rootfs.out")
	mustRun(t, "rlx", "uboot", back) // fixed-size partition
	if st, _ := os.Stat(back); st.Size() != 0x2000*512 {
		t.Fatalf("uboot size %d", st.Size())
	}
}

func TestUpgradeLoader(t *testing.T) {
	dev := useSim(t, usbfs.ModeLoader)
	idb, _ := os.ReadFile("../../testdata/idb-rk3128-append.img")
	mustRun(t, "ul", "../../testdata/idb-rk3128-append.img")
	if !bytes.Equal(dev.Disk[64*512:64*512+len(idb)], idb) {
		t.Fatal("ID block not at sector 64")
	}
}

func TestResetAndTest(t *testing.T) {
	dev := useSim(t, usbfs.ModeLoader)
	mustRun(t, "td")
	mustRun(t, "rd", "maskrom")
	mustRun(t, "reset")
	if len(dev.ResetSubs) != 2 || dev.ResetSubs[0] != rockusb.ResetMaskrom || dev.ResetSubs[1] != 0 {
		t.Fatalf("reset subcodes %v", dev.ResetSubs)
	}
}

func TestCapabilityUnsupported(t *testing.T) {
	dev := useSim(t, usbfs.ModeLoader)
	dev.NoCapab = true
	if _, errs, code := rk(t, "rcb"); code == 0 || !strings.Contains(errs, "old loaders") {
		t.Errorf("rcb on old loader: %d %s", code, errs)
	}
}

func TestVerboseLogging(t *testing.T) {
	useSim(t, usbfs.ModeLoader)
	var logb bytes.Buffer
	logx.SetOutput(&logb)
	defer logx.SetOutput(os.Stderr)
	mustRun(t, "-vvv", "rid")
	log := logb.String()
	for _, s := range []string{"info ", "READ_FLASH_ID", "CSW tag", "trace"} {
		if !strings.Contains(log, s) {
			t.Errorf("log lacks %q:\n%s", s, log)
		}
	}
}

func TestDownloadBoot(t *testing.T) {
	// RKFLASH_TEST_RKBIN may name a checkout of rockchip-linux/rkbin.
	rk := os.Getenv("RKFLASH_TEST_RKBIN")
	if rk == "" {
		t.Skip("RKFLASH_TEST_RKBIN not set")
	}
	loader := filepath.Join(rk, "bin/rk31/rk3128_loader_v2.05.240.bin")
	if _, err := os.Stat(loader); err != nil {
		loader = filepath.Join(rk, "rk31/rk3128_loader_v2.05.240.bin")
	}
	if _, err := os.Stat(loader); err != nil {
		t.Skip("rk3128 loader not found in RKFLASH_TEST_RKBIN")
	}
	dev := useSim(t, usbfs.ModeMaskrom)
	out := mustRun(t, "db", loader)
	if !strings.Contains(out, "Loader mode") {
		t.Errorf("db output: %s", out)
	}
	data, _ := os.ReadFile(loader)
	bf, _ := rkimage.ParseBootFile(data)
	for code, e := range map[uint16][]byte{rockusb.Req471: bf.Entries471[0].Data, rockusb.Req472: bf.Entries472[0].Data} {
		var got []byte
		for _, ch := range dev.Requests[code] {
			got = append(got, ch...)
		}
		want, _ := rockusb.MaskromPayload(e)
		if !bytes.Equal(got, want) {
			t.Errorf("request %#x: sent %d bytes, want %d", code, len(got), len(want))
		}
	}
}

func TestImageTools(t *testing.T) {
	dir := t.TempDir()
	idb := filepath.Join(dir, "idb.img")
	mustRun(t, "idb", "-chip", "rk3128", "-init", "../../testdata/init.bin", "-boot", "../../testdata/boot.bin", "-o", idb)
	got, _ := os.ReadFile(idb)
	want, _ := os.ReadFile("../../testdata/idb-rk3128-append.img")
	if !bytes.Equal(got, want) {
		t.Fatal("idb differs from mkimage reference")
	}
	if out := mustRun(t, "image-info", idb); !strings.Contains(out, "ID block") || !strings.Contains(out, `"RK31"`) {
		t.Errorf("image-info idb: %s", out)
	}

	payload := writeTemp(t, "u-boot.bin", pattern(100000))
	ub := filepath.Join(dir, "uboot.img")
	mustRun(t, "pack-uboot", "-addr", "0x60000000", "-o", ub, payload)
	if out := mustRun(t, "ii", ub); !strings.Contains(out, "LOADER image") || !strings.Contains(out, "CRC") || strings.Contains(out, "BAD") {
		t.Errorf("image-info uboot: %s", out)
	}
	back := filepath.Join(dir, "back.bin")
	mustRun(t, "unpack-uboot", ub, back)
	if b, _ := os.ReadFile(back); !bytes.Equal(b, pattern(100000)) {
		t.Fatal("unpack-uboot payload differs")
	}

	disk := filepath.Join(dir, "disk.img")
	mustRun(t, "gpt", "-o", disk, "-disk-size", "64M", "../../testdata/parameter.txt")
	if out := mustRun(t, "ppt", "-f", disk); !strings.Contains(out, "gpt") || !strings.Contains(out, "resource") {
		t.Errorf("ppt -f: %s", out)
	}
	if out := mustRun(t, "ii", "../../testdata/parameter.txt"); !strings.Contains(out, "parameter text") || !strings.Contains(out, "rk3128_box") {
		t.Errorf("image-info parameter: %s", out)
	}
}

func TestPackUnpackLoader(t *testing.T) {
	dir := t.TempDir()
	loader := filepath.Join(dir, "loader.bin")
	mustRun(t, "pack", "-o", loader, "-chip", "312A", "-version", "2.40",
		"-471", "../../testdata/init.bin:1", "-472", "../../testdata/boot.bin",
		"-flash-data", "../../testdata/init.bin", "-flash-boot", "../../testdata/boot.bin")
	out := mustRun(t, "ii", loader)
	if !strings.Contains(out, "Chip: 312A") || !strings.Contains(out, "Version: 2.40") || !strings.Contains(out, "FlashBoot") {
		t.Errorf("image-info loader: %s", out)
	}
	ex := filepath.Join(dir, "x")
	mustRun(t, "unpack", "-o", ex, loader)
	got, _ := os.ReadFile(filepath.Join(ex, "FlashBoot.bin"))
	boot, _ := os.ReadFile("../../testdata/boot.bin")
	if !bytes.HasPrefix(got, boot) {
		t.Fatal("unpacked FlashBoot differs")
	}
	// The ID block from this loader equals the one built from the same binaries.
	idb := filepath.Join(dir, "idb.img")
	mustRun(t, "idb", "-loader", loader, "-o", idb)
	a, _ := os.ReadFile(idb)
	want, _ := os.ReadFile("../../testdata/idb-rk3128-append.img")
	if !bytes.HasPrefix(a, want) {
		t.Fatal("idb from loader differs")
	}
}
