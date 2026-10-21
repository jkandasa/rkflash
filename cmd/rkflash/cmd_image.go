package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jkandasa/rkflash/internal/part"
	"github.com/jkandasa/rkflash/internal/rkimage"
)

func init() {
	register(&command{
		name: "image-info", aliases: []string{"ii"}, group: "Images", args: "<file>", nargs: [2]int{1, 1},
		summary: "Identify and check a Rockchip image file",
		help: `Recognizes loader files (BOOT), ID blocks (idbloader), LOADER/TOS images
(uboot.img, trust.img), parameter files and blocks, and disk images with a
GPT, MBR, or PARM table plus an ID block at sector 64.`,
		run: func(c *cmdCtx) error { return imageInfo(c.out, c.args[0]) },
	})

	var unpackDir string
	var unpackRaw bool
	register(&command{
		name: "unpack", group: "Images", args: "<loader.bin>", nargs: [2]int{1, 1},
		summary: "Extract the entries of a loader file",
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&unpackDir, "o", ".", "output `directory`")
			fs.BoolVar(&unpackRaw, "raw", false, "keep entries RC4-encrypted as stored in the file")
		},
		run: func(c *cmdCtx) error {
			data, err := os.ReadFile(c.args[0])
			if err != nil {
				return err
			}
			bf, err := rkimage.ParseBootFile(data)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(unpackDir, 0o755); err != nil {
				return err
			}
			for _, g := range [][]rkimage.BootEntry{bf.Entries471, bf.Entries472, bf.EntriesLoader} {
				for _, e := range g {
					d := append([]byte(nil), e.Data...)
					if unpackRaw {
						if e.Type == rkimage.EntryLoader {
							rkimage.RC4Blocks(d)
						} else {
							rkimage.RC4(d)
						}
					}
					name := filepath.Join(unpackDir, sanitize(e.Name)+".bin")
					if err := os.WriteFile(name, d, 0o644); err != nil {
						return err
					}
					fmt.Fprintf(c.out, "%-6s %-28s %8d bytes  delay %d ms\n", entryKind(e.Type), name, len(d), e.Delay)
				}
			}
			return nil
		},
	})

	var packOut, packChip, packVersion string
	var pack471, pack472, packData, packBoot multiFlag
	var packRC4 bool
	register(&command{
		name: "pack", group: "Images", nargs: [2]int{0, 0},
		summary: "Build a loader file (for 'db' and 'ul') from binaries",
		help: `Builds a Rockchip loader file like boot_merger. Each binary may carry a
delay in ms after a colon (used for 0x471/0x472), e.g. ddr.bin:1.

Example (RK3128, rkbin):
  rkflash pack -o rk3128_loader.bin -chip 312A -version 2.40 \
    -471 rk3128_ddr_300MHz_v2.06.bin:1 -472 rk3128_usbplug_v2.40.bin \
    -flash-data rk3128_ddr_300MHz_v2.06.bin -flash-boot rk312x_miniloader_v2.40.bin`,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&packOut, "o", "", "output `file` (required)")
			fs.StringVar(&packChip, "chip", "", "4-character chip tag, e.g. 312A (required)")
			fs.StringVar(&packVersion, "version", "1.0", "loader version major.minor")
			fs.Var(&pack471, "471", "DDR init `file[:delay]` for Maskrom download (repeatable)")
			fs.Var(&pack472, "472", "USB plug `file[:delay]` for Maskrom download (repeatable)")
			fs.Var(&packData, "flash-data", "DDR init `file` for the ID block (FlashData)")
			fs.Var(&packBoot, "flash-boot", "miniloader/SPL `file` for the ID block (FlashBoot)")
			fs.BoolVar(&packRC4, "rc4-flag", false, "set the header RC4 flag")
		},
		run: func(c *cmdCtx) error {
			if packOut == "" || packChip == "" {
				return fmt.Errorf("-o and -chip are required")
			}
			ct, err := rkimage.ChipTypeFromTag(packChip)
			if err != nil {
				return err
			}
			ver, err := parseLoaderVersion(packVersion)
			if err != nil {
				return err
			}
			bf := &rkimage.BootFile{ChipType: ct, Version: ver, MergerVersion: 0x01030000, Time: time.Now().UTC()}
			if packRC4 {
				bf.RC4Flag = 1
			}
			add := func(list multiFlag, typ uint32, fixedName string) ([]rkimage.BootEntry, error) {
				var out []rkimage.BootEntry
				for _, spec := range list {
					path, delay := spec, uint32(0)
					if i := strings.LastIndex(spec, ":"); i > 0 {
						if d, err := strconv.ParseUint(spec[i+1:], 0, 32); err == nil {
							path, delay = spec[:i], uint32(d)
						}
					}
					data, err := os.ReadFile(path)
					if err != nil {
						return nil, err
					}
					name := fixedName
					if name == "" {
						name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
					}
					out = append(out, rkimage.BootEntry{Type: typ, Name: name, Delay: delay, Data: data})
				}
				return out, nil
			}
			if bf.Entries471, err = add(pack471, rkimage.Entry471, ""); err != nil {
				return err
			}
			if bf.Entries472, err = add(pack472, rkimage.Entry472, ""); err != nil {
				return err
			}
			fd, err := add(packData, rkimage.EntryLoader, "FlashData")
			if err != nil {
				return err
			}
			fb, err := add(packBoot, rkimage.EntryLoader, "FlashBoot")
			if err != nil {
				return err
			}
			bf.EntriesLoader = append(fd, fb...)
			if len(bf.Entries471) == 0 || len(bf.Entries472) == 0 {
				return fmt.Errorf("-471 and -472 are required")
			}
			out, err := bf.Marshal()
			if err != nil {
				return err
			}
			if err := os.WriteFile(packOut, out, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(c.out, "Wrote %s (%s)\n", packOut, humanBytes(uint64(len(out))))
			return nil
		},
	})

	var idbOut, idbChip, idbInit, idbBoot, idbLoader string
	var idbSplit bool
	register(&command{
		name: "idb", group: "Images", nargs: [2]int{0, 0},
		summary: "Build an ID block (idbloader.img), compatible with mkimage -T rksd",
		help: `Builds the ID block that the boot ROM loads from sector 64.

  rkflash idb -chip rk3128 -init ddr.bin -boot miniloader.bin -o idbloader.img
  rkflash idb -loader rk3128_loader_v2.05.240.bin -o idbloader.img

By default the boot stage is appended after the init code with a 512 KiB
init+boot size ("mkimage -d ddr.bin" plus "cat miniloader.bin"). With -split
the boot stage is padded to 2 KiB ("mkimage -d ddr.bin:spl.bin").

Chips: ` + chipList(),
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&idbOut, "o", "", "output `file` (required)")
			fs.StringVar(&idbChip, "chip", "", "chip name, e.g. rk3128")
			fs.StringVar(&idbInit, "init", "", "DDR init / TPL `file`")
			fs.StringVar(&idbBoot, "boot", "", "miniloader / SPL `file` (optional)")
			fs.StringVar(&idbLoader, "loader", "", "take init and boot from a loader `file`")
			fs.BoolVar(&idbSplit, "split", false, "pad the boot stage like mkimage -d init:boot")
		},
		run: func(c *cmdCtx) error {
			if idbOut == "" {
				return fmt.Errorf("-o is required")
			}
			var out []byte
			if idbLoader != "" {
				data, err := os.ReadFile(idbLoader)
				if err != nil {
					return err
				}
				if out, err = idbFromInput(data, idbChip); err != nil {
					return err
				}
			} else {
				if idbChip == "" || idbInit == "" {
					return fmt.Errorf("-chip and -init are required (or use -loader)")
				}
				chip, err := rkimage.FindChip(idbChip)
				if err != nil {
					return err
				}
				initCode, err := os.ReadFile(idbInit)
				if err != nil {
					return err
				}
				var boot []byte
				if idbBoot != "" {
					if boot, err = os.ReadFile(idbBoot); err != nil {
						return err
					}
				}
				if out, err = rkimage.BuildIDB(chip, initCode, boot, rkimage.IDBOptions{Append: !idbSplit}); err != nil {
					return err
				}
			}
			if err := os.WriteFile(idbOut, out, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(c.out, "Wrote %s (%s)\n", idbOut, humanBytes(uint64(len(out))))
			return nil
		},
	})

	var puOut, puMagic, puAddr, puCopySize string
	var puHash, puCopies int
	register(&command{
		name: "pack-uboot", group: "Images", args: "<u-boot.bin>", nargs: [2]int{1, 1},
		summary: "Wrap a binary as uboot.img/trust.img (like loaderimage --pack)",
		help: `The Rockchip miniloader loads U-Boot from such an image, usually at
sector 0x4000 (8 MiB), and checks its CRC and hash.

  rkflash pack-uboot -addr 0x60000000 -o uboot.img u-boot-dtb.bin`,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&puOut, "o", "", "output `file` (required)")
			fs.StringVar(&puMagic, "magic", "LOADER", "LOADER (U-Boot) or TOS (trust OS)")
			fs.StringVar(&puAddr, "addr", "", "load `address` (required)")
			fs.IntVar(&puHash, "hash", 32, "hash: 32 (SHA-256), 20 (SHA-1), or 0")
			fs.IntVar(&puCopies, "copies", 4, "number of copies")
			fs.StringVar(&puCopySize, "copy-size", "1M", "size of one copy")
		},
		run: func(c *cmdCtx) error {
			if puOut == "" || puAddr == "" {
				return fmt.Errorf("-o and -addr are required")
			}
			addr, err := parseUint32(puAddr)
			if err != nil {
				return err
			}
			cs, err := parseSectors(puCopySize)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(c.args[0])
			if err != nil {
				return err
			}
			out, err := rkimage.PackLoaderImage(data, rkimage.LoaderImageOptions{Magic: strings.ToUpper(puMagic),
				LoadAddr: addr, HashLen: puHash, CopySize: int(cs * sectorSize), Copies: puCopies})
			if err != nil {
				return err
			}
			if err := os.WriteFile(puOut, out, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(c.out, "Wrote %s (%s, %d copies, load 0x%08x)\n", puOut, humanBytes(uint64(len(out))), puCopies, addr)
			return nil
		},
	})

	register(&command{
		name: "unpack-uboot", group: "Images", args: "<uboot.img> <out.bin>", nargs: [2]int{2, 2},
		summary: "Extract the payload of a uboot.img/trust.img",
		run: func(c *cmdCtx) error {
			data, err := os.ReadFile(c.args[0])
			if err != nil {
				return err
			}
			payload, li, err := rkimage.UnpackLoaderImage(data)
			if err != nil {
				return err
			}
			if !li.CRCOK {
				fmt.Fprintln(c.out, "Warning: CRC mismatch")
			}
			if err := os.WriteFile(c.args[1], payload, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(c.out, "Wrote %s (%s, load 0x%08x)\n", c.args[1], humanBytes(uint64(len(payload))), li.LoadAddr)
			return nil
		},
	})

	register(&command{
		name: "udev-rule", group: "Other", nargs: [2]int{0, 0},
		summary: "Print a udev rule for access without root",
		help: `Install it with:
  rkflash udev-rule | sudo tee /etc/udev/rules.d/99-rockchip-usb.rules
  sudo udevadm control --reload-rules && sudo udevadm trigger`,
		run: func(c *cmdCtx) error {
			fmt.Fprintln(c.out, `# Rockchip devices in Maskrom and Loader mode
SUBSYSTEM=="usb", ATTR{idVendor}=="2207", MODE="0660", TAG+="uaccess"`)
			return nil
		},
	})

	register(&command{
		name: "version", group: "Other", nargs: [2]int{0, 0},
		summary: "Print version and build details",
		run: func(c *cmdCtx) error {
			printVersion(c.out)
			return nil
		},
	})
}

// multiFlag collects repeated string flags.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }

func parseLoaderVersion(s string) (uint32, error) {
	parts := strings.SplitN(s, ".", 2)
	maj, err := strconv.ParseUint(parts[0], 10, 8)
	if err != nil {
		return 0, fmt.Errorf("bad version %q", s)
	}
	var mnr uint64
	if len(parts) == 2 {
		// Rockchip stores BCD, e.g. 2.40 -> 0x240.
		if mnr, err = strconv.ParseUint(parts[1], 16, 8); err != nil {
			return 0, fmt.Errorf("bad version %q", s)
		}
	}
	return uint32(maj<<8 | mnr), nil
}

func chipList() string {
	var names []string
	for _, ch := range rkimage.Chips {
		names = append(names, ch.Name)
	}
	return strings.Join(names, ", ")
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < ' ' {
			return '_'
		}
		return r
	}, s)
}

func entryKind(t uint32) string {
	switch t {
	case rkimage.Entry471:
		return "471"
	case rkimage.Entry472:
		return "472"
	case rkimage.EntryLoader:
		return "flash"
	}
	return fmt.Sprint(t)
}

// imageInfo describes a file of any supported type.
func imageInfo(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	// 16 MiB covers the ID block and the U-Boot/trust slots of disk images.
	data := make([]byte, min(st.Size(), 16<<20))
	if _, err := io.ReadFull(f, data); err != nil {
		return err
	}
	fmt.Fprintf(w, "File: %s (%s)\n", path, humanBytes(uint64(st.Size())))
	if len(data) >= 4 && (string(data[:4]) == "BOOT" || string(data[:4]) == "LDR ") {
		bf, err := rkimage.ParseBootFile(data)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "Type: Rockchip loader file (%s), CRC OK\n", bf.Tag)
		fmt.Fprintf(w, "Chip: %s  Version: %x.%02x  Built: %s  RC4 flag: %d  Sign flag: %d\n",
			bf.Chip(), bf.Version>>8, bf.Version&0xff, bf.Time.Format("2006-01-02 15:04:05"), bf.RC4Flag, bf.SignFlag)
		for _, g := range [][]rkimage.BootEntry{bf.Entries471, bf.Entries472, bf.EntriesLoader} {
			for _, e := range g {
				fmt.Fprintf(w, "  %-6s %-24s %8d bytes  delay %d ms%s\n", entryKind(e.Type), e.Name, len(e.Data), e.Delay, versionHint(e.Data))
			}
		}
		return nil
	}
	if li, err := rkimage.ParseLoaderImage(data); err == nil {
		fmt.Fprintf(w, "Type: %s image (uboot.img/trust.img format)\n", strings.TrimSpace(li.Magic))
		fmt.Fprintf(w, "Load address: 0x%08x  Payload: %s  Copies: %d\n", li.LoadAddr, humanBytes(uint64(li.Size)), li.Copies)
		fmt.Fprintf(w, "CRC: %08x %s  Hash: %d bytes %s  RSA signed: %v\n", li.CRC, okBad(li.CRCOK), li.HashLen, okBad(li.HashOK), li.Signed)
		return nil
	}
	if text, err := rkimage.DecodeParmBlock(data); err == nil || (len(data) >= 4 && string(data[:4]) == "PARM") {
		fmt.Fprintf(w, "Type: parameter block (PARM), CRC %s\n", okBad(err == nil))
		return describeParameter(w, text)
	}
	if info, err := rkimage.ParseIDB(data); err == nil {
		describeIDB(w, "Type: ID block (idbloader)", info, data)
		return nil
	}
	if len(data) >= 34*sectorSize {
		head := data[:34*sectorSize]
		if t, err := part.Detect(head); err == nil {
			fmt.Fprintln(w, "Type: disk image")
			printTable(w, t)
			off := rkimage.IDBSector * sectorSize
			if len(data) > off+512 {
				if info, err := rkimage.ParseIDB(data[off:]); err == nil {
					describeIDB(w, "ID block at sector 64", info, data[off:])
				}
			}
			for _, s := range []int{0x2000, 0x4000, 0x6000} {
				if o := s * sectorSize; len(data) > o+0x800 {
					if li, err := rkimage.ParseLoaderImage(data[o:]); err == nil {
						fmt.Fprintf(w, "%s image at sector 0x%x: load 0x%08x, %s, CRC %s\n",
							strings.TrimSpace(li.Magic), s, li.LoadAddr, humanBytes(uint64(li.Size)), okBad(li.CRCOK))
					}
				}
			}
			return nil
		}
	}
	if strings.Contains(string(data[:min(len(data), 4096)]), "mtdparts=") {
		fmt.Fprintln(w, "Type: parameter text file")
		return describeParameter(w, string(data))
	}
	return fmt.Errorf("unknown file type")
}

func describeIDB(w io.Writer, title string, info *rkimage.IDBInfo, data []byte) {
	fmt.Fprintf(w, "%s: tag %q, init at sector %d, init %d sectors, init+boot %d sectors, RC4 %s%s\n",
		title, info.SPLHdr, info.InitOffset, info.InitSize, info.InitBootSize,
		map[bool]string{true: "off", false: "on"}[info.DisableRC4], versionHint(data))
}

func describeParameter(w io.Writer, text string) error {
	p, err := rkimage.ParseParameter(text)
	for _, k := range []string{"FIRMWARE_VER", "MACHINE_MODEL", "MACHINE", "TYPE"} {
		if v, ok := p.Lines[k]; ok {
			fmt.Fprintf(w, "%s: %s\n", k, v)
		}
	}
	if err != nil {
		return err
	}
	t := &part.Table{Kind: "parameter"}
	for i, mp := range p.Parts {
		t.Partitions = append(t.Partitions, part.Partition{Index: i + 1, Name: mp.Name, Start: mp.Offset, Size: mp.Size})
	}
	printTable(w, t)
	return nil
}

func okBad(ok bool) string {
	if ok {
		return "OK"
	}
	return "BAD"
}

// versionHint finds "DDR Version ..." or "U-Boot ..." strings in a binary.
func versionHint(b []byte) string {
	for _, key := range []string{"DDR Version ", "U-Boot 20", "Boot1 Release Time"} {
		if i := strings.Index(string(b), key); i >= 0 {
			end := i
			for end < len(b) && end-i < 60 && b[end] >= ' ' && b[end] < 0x7f {
				end++
			}
			return "  [" + string(b[i:end]) + "]"
		}
	}
	return ""
}
