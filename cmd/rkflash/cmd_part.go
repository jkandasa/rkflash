package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/jkandasa/rkflash/internal/logx"
	"github.com/jkandasa/rkflash/internal/part"
	"github.com/jkandasa/rkflash/internal/rkimage"
	"github.com/jkandasa/rkflash/internal/rockusb"
)

func init() {
	var pptFile string
	register(&command{
		name: "print-partitions", aliases: []string{"ppt"}, group: "Partitions", nargs: [2]int{0, 0},
		summary: "Print the partition table (GPT, MBR, or RK parameter)",
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&pptFile, "f", "", "read from a disk image `file` instead of the device")
		},
		run: func(c *cmdCtx) error {
			var t *part.Table
			var err error
			if pptFile != "" {
				t, err = tableFromFile(pptFile)
			} else {
				cl, closeFn, e := openLoader(c.g)
				if e != nil {
					return e
				}
				defer closeFn()
				t, err = readTable(c, cl)
			}
			if err != nil {
				return err
			}
			printTable(c.out, t)
			return nil
		},
	})

	var gptOut, gptDisk string
	var gptDry bool
	register(&command{
		name: "gpt", group: "Partitions", args: "<parameter.txt>", nargs: [2]int{1, 1},
		summary: "Write a GPT built from a Rockchip parameter file",
		help: `Converts the mtdparts of a parameter file to a GPT and writes the primary
and backup tables. A partition with size "-" grows to the end of the disk.
Lines "uuid:<name>=<GUID>" set partition GUIDs, e.g. for rootfs.

Example parameter line:
  CMDLINE: mtdparts=rk29xxnand:0x2000@0x4000(uboot),0x2000@0x6000(trust),-@0x8000(rootfs:grow)`,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&gptOut, "o", "", "write into a disk image `file` instead of the device")
			fs.StringVar(&gptDisk, "disk-size", "", "disk size for a new image file (e.g. 8G)")
			fs.BoolVar(&gptDry, "dry-run", false, "print the table, write nothing")
		},
		run: func(c *cmdCtx) error {
			prm, err := loadParameter(c.args[0])
			if err != nil {
				return err
			}
			entries := part.GPTFromParameter(prm)
			var target sectorTarget
			var closeFn func()
			if gptOut != "" {
				target, closeFn, err = openImageTarget(gptOut, gptDisk)
			} else {
				target, closeFn, err = deviceTarget(c)
			}
			if err != nil {
				return err
			}
			defer closeFn()
			img, err := part.BuildGPT(target.sectors(), entries)
			if err != nil {
				return err
			}
			printTable(c.out, &part.Table{Kind: "gpt", Partitions: img.Partitions})
			if gptDry {
				return nil
			}
			if gptOut == "" {
				if err := confirm(c.g, "Write this GPT to the device?"); err != nil {
					return err
				}
			}
			if err := target.write(0, img.Primary); err != nil {
				return err
			}
			if err := target.write(img.BackupLBA, img.Backup); err != nil {
				return err
			}
			fmt.Fprintln(c.out, "GPT written (primary and backup).")
			return nil
		},
	})

	var prmOut string
	var prmCopies int
	register(&command{
		name: "parameter", aliases: []string{"prm"}, group: "Partitions", args: "<parameter.txt>", nargs: [2]int{1, 1},
		summary: "Write a legacy RK parameter block (PARM) at sector 0",
		help: `Legacy Rockchip firmware reads its partition table from a PARM block at
sector 0, with copies every 1024 sectors. This overwrites any MBR or GPT.`,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&prmOut, "o", "", "write into an image `file` instead of the device")
			fs.IntVar(&prmCopies, "copies", rkimage.ParmCopies, "number of copies, 1024 sectors apart")
		},
		run: func(c *cmdCtx) error {
			text, err := os.ReadFile(c.args[0])
			if err != nil {
				return err
			}
			if _, err := rkimage.ParseParameter(string(text)); err != nil {
				return fmt.Errorf("%s: %w", c.args[0], err)
			}
			blk := rkimage.EncodeParmBlock(string(text))
			blk = append(blk, make([]byte, (sectorSize-len(blk)%sectorSize)%sectorSize)...)
			if len(blk) > rkimage.ParmStride*sectorSize {
				return fmt.Errorf("parameter is too large")
			}
			var target sectorTarget
			var closeFn func()
			if prmOut != "" {
				target, closeFn, err = openImageTarget(prmOut, "")
			} else {
				target, closeFn, err = deviceTarget(c)
				if err == nil {
					err = confirm(c.g, "Write the parameter block (overwrites any MBR/GPT)?")
				}
			}
			if err != nil {
				return err
			}
			defer closeFn()
			for i := 0; i < prmCopies; i++ {
				if err := target.write(uint64(i*rkimage.ParmStride), blk); err != nil {
					return err
				}
			}
			fmt.Fprintf(c.out, "Parameter written (%d copies).\n", prmCopies)
			return nil
		},
	})
}

func loadParameter(path string) (*rkimage.Parameter, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p, err := rkimage.ParseParameter(string(text))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

func printTable(w io.Writer, t *part.Table) {
	fmt.Fprintf(w, "Partition table: %s", t.Kind)
	if t.DiskGUID != "" {
		fmt.Fprintf(w, " (disk GUID %s)", t.DiskGUID)
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "#\tNAME\tSTART\tEND\tSECTORS\tSIZE\tTYPE")
	for _, p := range t.Partitions {
		end, size := fmt.Sprint(p.End()), humanBytes(p.Size*sectorSize)
		if p.Size == 0 {
			end, size = "-", "grow"
		}
		fmt.Fprintf(tw, "%d\t%s\t0x%x\t%s\t%d\t%s\t%s\n", p.Index, p.Name, p.Start, end, p.Size, size, p.Type)
	}
	tw.Flush()
}

func tableFromFile(path string) (*part.Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, 34*sectorSize)
	if _, err := io.ReadFull(f, head); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return part.Detect(head)
}

// sectorTarget is a device or an image file addressed in sectors.
type sectorTarget interface {
	sectors() uint64
	write(lba uint64, data []byte) error
}

type devTarget struct {
	c     *cmdCtx
	cl    *rockusb.Client
	total uint64
}

func (d *devTarget) sectors() uint64 { return d.total }
func (d *devTarget) write(lba uint64, data []byte) error {
	logx.Infof("writing %d bytes at sector %d", len(data), lba)
	return writeBuffer(d.c, d.cl, lba, data, true, "write")
}

func deviceTarget(c *cmdCtx) (sectorTarget, func(), error) {
	cl, closeFn, err := openLoader(c.g)
	if err != nil {
		return nil, nil, err
	}
	fi, err := cl.FlashInfo()
	if err != nil {
		closeFn()
		return nil, nil, err
	}
	return &devTarget{c: c, cl: cl, total: uint64(fi.Sectors)}, closeFn, nil
}

type fileTarget struct {
	f     *os.File
	total uint64
}

func (t *fileTarget) sectors() uint64 { return t.total }
func (t *fileTarget) write(lba uint64, data []byte) error {
	_, err := t.f.WriteAt(data, int64(lba*sectorSize))
	return err
}

// openImageTarget opens an image file for in-place writes. A new file needs a size.
func openImageTarget(path, size string) (sectorTarget, func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	total := uint64(st.Size()) / sectorSize
	if size != "" {
		n, err := parseSectors(size)
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		if n > total {
			if err := f.Truncate(int64(n * sectorSize)); err != nil {
				f.Close()
				return nil, nil, err
			}
		}
		total = n
	}
	if total == 0 {
		f.Close()
		return nil, nil, fmt.Errorf("%s is empty; pass -disk-size", path)
	}
	return &fileTarget{f: f, total: total}, func() { f.Close() }, nil
}
