package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jkandasa/rkflash/internal/logx"
	"github.com/jkandasa/rkflash/internal/part"
	"github.com/jkandasa/rkflash/internal/rockusb"
)

func init() {
	register(&command{
		name: "read", aliases: []string{"rl"}, group: "Storage", args: "<start> <count|size|all> <file>", nargs: [2]int{3, 3},
		summary: "Read sectors to a file",
		help: `Examples:
  rkflash read 0 all emmc-backup.img     whole storage
  rkflash read 0x2000 4M uboot.img       4 MiB from sector 0x2000`,
		run: func(c *cmdCtx) error {
			cl, closeFn, err := openLoader(c.g)
			if err != nil {
				return err
			}
			defer closeFn()
			start, count, err := resolveRange(cl, c.args[0], c.args[1])
			if err != nil {
				return err
			}
			return readToFile(c, cl, start, count, c.args[2], "read")
		},
	})

	var wlVerify bool
	register(&command{
		name: "write", aliases: []string{"wl"}, group: "Storage", args: "<start> <file>", nargs: [2]int{2, 2},
		summary: "Write a file at a sector",
		flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&wlVerify, "verify", false, "read back and compare each chunk")
		},
		run: func(c *cmdCtx) error {
			start, err := parseSectors(c.args[0])
			if err != nil {
				return err
			}
			cl, closeFn, err := openLoader(c.g)
			if err != nil {
				return err
			}
			defer closeFn()
			return writeFile(c, cl, start, c.args[1], 0, wlVerify, "write")
		},
	})

	register(&command{
		name: "erase", aliases: []string{"el"}, group: "Storage", args: "<start> <count|size|all>", nargs: [2]int{2, 2},
		summary: "Erase sectors with the ERASE_LBA command",
		run: func(c *cmdCtx) error {
			cl, closeFn, err := openLoader(c.g)
			if err != nil {
				return err
			}
			defer closeFn()
			start, count, err := resolveRange(cl, c.args[0], c.args[1])
			if err != nil {
				return err
			}
			return eraseRange(c, cl, start, count)
		},
	})

	register(&command{
		name: "erase-flash", aliases: []string{"ef"}, group: "Storage", nargs: [2]int{0, 0},
		summary: "Erase the whole storage (slow on eMMC; see 'zero')",
		run: func(c *cmdCtx) error {
			cl, closeFn, err := openLoader(c.g)
			if err != nil {
				return err
			}
			defer closeFn()
			fi, err := cl.FlashInfo()
			if err != nil {
				return err
			}
			if err := confirm(c.g, "Erase all %s of storage?", humanBytes(fi.Bytes())); err != nil {
				return err
			}
			return eraseRange(c, cl, 0, uint64(fi.Sectors))
		},
	})

	register(&command{
		name: "zero", group: "Storage", args: "<start> <count|size|all>", nargs: [2]int{2, 2},
		summary: "Overwrite sectors with zeros (fast wipe of a region)",
		help: `Writes zeros instead of erasing. On eMMC this is much faster than
erase-flash for small regions, e.g. "rkflash zero 0 32M" removes the ID block,
U-Boot, and the partition table.`,
		run: func(c *cmdCtx) error {
			cl, closeFn, err := openLoader(c.g)
			if err != nil {
				return err
			}
			defer closeFn()
			start, count, err := resolveRange(cl, c.args[0], c.args[1])
			if err != nil {
				return err
			}
			return writeStream(c, cl, start, zeroReader{}, count*sectorSize, false, "zero")
		},
	})

	register(&command{
		name: "read-part", aliases: []string{"rlx"}, group: "Storage", args: "<partition> <file>", nargs: [2]int{2, 2},
		summary: "Read a partition (by name) to a file",
		run: func(c *cmdCtx) error {
			cl, closeFn, err := openLoader(c.g)
			if err != nil {
				return err
			}
			defer closeFn()
			p, err := findPartition(c, cl, c.args[0])
			if err != nil {
				return err
			}
			return readToFile(c, cl, p.Start, p.Size, c.args[1], "read")
		},
	})

	var wlxVerify bool
	register(&command{
		name: "write-part", aliases: []string{"wlx"}, group: "Storage", args: "<partition> <file>", nargs: [2]int{2, 2},
		summary: "Write a file to a partition (by name)",
		flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&wlxVerify, "verify", false, "read back and compare each chunk")
		},
		run: func(c *cmdCtx) error {
			cl, closeFn, err := openLoader(c.g)
			if err != nil {
				return err
			}
			defer closeFn()
			p, err := findPartition(c, cl, c.args[0])
			if err != nil {
				return err
			}
			return writeFile(c, cl, p.Start, c.args[1], p.Size, wlxVerify, "write")
		},
	})
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// resolveRange parses start and count; "all" means to the end of storage.
func resolveRange(cl *rockusb.Client, startS, countS string) (uint64, uint64, error) {
	start, err := parseSectors(startS)
	if err != nil {
		return 0, 0, err
	}
	fi, err := cl.FlashInfo()
	if err != nil {
		return 0, 0, fmt.Errorf("read flash info: %w", err)
	}
	total := uint64(fi.Sectors)
	if start >= total {
		return 0, 0, fmt.Errorf("start sector %d is beyond the storage end (%d sectors)", start, total)
	}
	if countS == "all" {
		return start, total - start, nil
	}
	count, err := parseSectors(countS)
	if err != nil {
		return 0, 0, err
	}
	if start+count > total {
		return 0, 0, fmt.Errorf("range %d+%d is beyond the storage end (%d sectors)", start, count, total)
	}
	return start, count, nil
}

func checkLBA(end uint64) error {
	if end > 1<<32 {
		return fmt.Errorf("sector %d is beyond the 32-bit LBA limit of the protocol", end)
	}
	return nil
}

func readToFile(c *cmdCtx, cl *rockusb.Client, start, count uint64, path string, label string) error {
	if err := checkLBA(start + count); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	logx.Infof("reading %d sectors at %d to %s", count, start, path)
	chunk := uint64(c.g.chunk)
	buf := make([]byte, chunk*sectorSize)
	p := newProgress(c.g, label, count*sectorSize)
	for done := uint64(0); done < count; {
		if err := c.ctx.Err(); err != nil {
			f.Close()
			return fmt.Errorf("stopped at sector %d: %w", start+done, err)
		}
		n := min(chunk, count-done)
		b := buf[:n*sectorSize]
		if err := cl.ReadLBA(uint32(start+done), uint16(n), b); err != nil {
			f.Close()
			return fmt.Errorf("read at sector %d: %w", start+done, err)
		}
		if _, err := f.Write(b); err != nil {
			f.Close()
			return err
		}
		done += n
		p.add(n * sectorSize)
	}
	p.finish(c.out)
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// writeFile writes a file at a sector. limit > 0 caps the size in sectors.
func writeFile(c *cmdCtx, cl *rockusb.Client, start uint64, path string, limit uint64, verify bool, label string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := uint64(st.Size())
	sectors := (size + sectorSize - 1) / sectorSize
	if limit > 0 && sectors > limit {
		return fmt.Errorf("%s needs %d sectors, the partition has %d", path, sectors, limit)
	}
	fi, err := cl.FlashInfo()
	if err != nil {
		return fmt.Errorf("read flash info: %w", err)
	}
	if start+sectors > uint64(fi.Sectors) {
		return fmt.Errorf("%s needs %d sectors at %d, storage has %d", path, sectors, start, fi.Sectors)
	}
	logx.Infof("writing %s (%s) at sector %d", path, humanBytes(size), start)
	return writeStream(c, cl, start, f, size, verify, label)
}

// writeStream writes size bytes from r at a sector. The last sector is zero-padded.
func writeStream(c *cmdCtx, cl *rockusb.Client, start uint64, r io.Reader, size uint64, verify bool, label string) error {
	sectors := (size + sectorSize - 1) / sectorSize
	if err := checkLBA(start + sectors); err != nil {
		return err
	}
	chunk := uint64(c.g.chunk)
	buf := make([]byte, chunk*sectorSize)
	var rb []byte
	if verify {
		rb = make([]byte, chunk*sectorSize)
	}
	p := newProgress(c.g, label, sectors*sectorSize)
	remaining := size
	for done := uint64(0); done < sectors; {
		if err := c.ctx.Err(); err != nil {
			return fmt.Errorf("stopped at sector %d: %w", start+done, err)
		}
		n := min(chunk, sectors-done)
		b := buf[:n*sectorSize]
		want := min(remaining, uint64(len(b)))
		got, err := io.ReadFull(r, b[:want])
		if err != nil {
			return fmt.Errorf("read input: %w", err)
		}
		clear(b[got:])
		remaining -= uint64(got)
		lba := uint32(start + done)
		if err := cl.WriteLBA(lba, uint16(n), b); err != nil {
			return fmt.Errorf("write at sector %d: %w", lba, err)
		}
		if verify {
			v := rb[:len(b)]
			if err := cl.ReadLBA(lba, uint16(n), v); err != nil {
				return fmt.Errorf("verify read at sector %d: %w", lba, err)
			}
			if !bytes.Equal(b, v) {
				return fmt.Errorf("verify mismatch at sector %d", lba+uint32(firstDiff(b, v)/sectorSize))
			}
		}
		done += n
		p.add(n * sectorSize)
	}
	p.finish(c.out)
	if verify {
		fmt.Fprintln(c.out, "Verify OK.")
	}
	return nil
}

func firstDiff(a, b []byte) int {
	for i := range a {
		if a[i] != b[i] {
			return i
		}
	}
	return 0
}

func eraseRange(c *cmdCtx, cl *rockusb.Client, start, count uint64) error {
	if err := checkLBA(start + count); err != nil {
		return err
	}
	const chunk = 32768 // 16 MiB per command
	logx.Infof("erasing %d sectors at %d", count, start)
	p := newProgress(c.g, "erase", count*sectorSize)
	for done := uint64(0); done < count; {
		if err := c.ctx.Err(); err != nil {
			return fmt.Errorf("stopped at sector %d: %w", start+done, err)
		}
		n := min(uint64(chunk), count-done)
		if err := cl.EraseLBA(uint32(start+done), uint16(n)); err != nil {
			return fmt.Errorf("erase at sector %d: %w", start+done, err)
		}
		done += n
		p.add(n * sectorSize)
	}
	p.finish(c.out)
	return nil
}

// readTable reads the first sectors and detects the partition table.
func readTable(c *cmdCtx, cl *rockusb.Client) (*part.Table, error) {
	head := make([]byte, 34*sectorSize)
	if err := cl.ReadLBA(0, 34, head); err != nil {
		return nil, fmt.Errorf("read partition table: %w", err)
	}
	return part.Detect(head)
}

func findPartition(c *cmdCtx, cl *rockusb.Client, name string) (*part.Partition, error) {
	t, err := readTable(c, cl)
	if err != nil {
		return nil, err
	}
	p, err := t.Find(name)
	if err != nil {
		return nil, err
	}
	if p.Size == 0 { // parameter "grow" partition
		fi, err := cl.FlashInfo()
		if err != nil {
			return nil, err
		}
		p.Size = uint64(fi.Sectors) - p.Start
	}
	logx.Infof("partition %s (%s): sectors %d..%d", p.Name, t.Kind, p.Start, p.End())
	return p, nil
}
