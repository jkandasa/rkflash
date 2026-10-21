package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jkandasa/rkflash/internal/logx"
	"github.com/jkandasa/rkflash/internal/rkimage"
	"github.com/jkandasa/rkflash/internal/rockusb"
	"github.com/jkandasa/rkflash/internal/usbfs"
)

func init() {
	var listWait time.Duration
	register(&command{
		name: "list", aliases: []string{"ld"}, group: "Device", nargs: [2]int{0, 0},
		summary: "List Rockchip USB devices and their mode",
		flags: func(fs *flag.FlagSet) {
			fs.DurationVar(&listWait, "wait", 0, "wait up to this long for a device to appear")
		},
		run: func(c *cmdCtx) error {
			if listWait > 0 {
				if _, err := waitForMode(c.g, "any", listWait); err != nil {
					return err
				}
			}
			devs, err := listDevices()
			if err != nil {
				return err
			}
			if len(devs) == 0 {
				fmt.Fprintln(c.out, "No Rockchip device found.")
				return exitError{1}
			}
			tw := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "DEVICE\tPORT\tVID:PID\tMODE\tSOC")
			for _, d := range devs {
				soc := rockusb.SoCName(d.PID)
				if soc == "" {
					soc = "unknown"
				}
				fmt.Fprintf(tw, "%d:%d\t%s\t%04x:%04x\t%s\t%s\n", d.Bus, d.Dev, d.Port, usbfs.RockchipVID, d.PID, d.Mode(), soc)
			}
			return tw.Flush()
		},
	})

	var waitMode string
	var waitFor time.Duration
	register(&command{
		name: "wait", group: "Device", nargs: [2]int{0, 0},
		summary: "Wait for a device to appear in a given mode",
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&waitMode, "mode", "any", "mode to wait for: loader, maskrom, or any")
			fs.DurationVar(&waitFor, "for", 30*time.Second, "how long to wait")
		},
		run: func(c *cmdCtx) error {
			d, err := waitForMode(c.g, waitMode, waitFor)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.out, "%d:%d %s\n", d.Bus, d.Dev, d.Mode())
			return nil
		},
	})

	var dbNoWait bool
	register(&command{
		name: "download-boot", aliases: []string{"db"}, group: "Device", args: "<loader.bin>", nargs: [2]int{1, 1},
		summary: "Maskrom: load the USB loader (DDR init + USB plug) into RAM",
		help: `Sends the 0x471 (DDR init) and 0x472 (USB plug) entries of a Rockchip
loader file, e.g. rkbin rk3128_loader_v2.05.240.bin. Nothing is written to
storage. The device then re-enumerates in Loader mode.`,
		flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&dbNoWait, "no-wait", false, "do not wait for Loader mode afterwards")
		},
		run: func(c *cmdCtx) error {
			data, err := os.ReadFile(c.args[0])
			if err != nil {
				return err
			}
			bf, err := rkimage.ParseBootFile(data)
			if err != nil {
				return fmt.Errorf("%s: %w", c.args[0], err)
			}
			ctl, closeFn, err := openMaskrom(c.g)
			if err != nil {
				return err
			}
			fmt.Fprintf(c.out, "Loader chip %s, %d DDR + %d USB plug entries\n", bf.Chip(), len(bf.Entries471), len(bf.Entries472))
			err = rockusb.DownloadBoot(ctl, bf, func(e *rkimage.BootEntry) {
				fmt.Fprintf(c.out, "  download %-24s %8d bytes\n", e.Name, len(e.Data))
			})
			closeFn()
			if err != nil {
				return err
			}
			if dbNoWait {
				fmt.Fprintln(c.out, "Download done.")
				return nil
			}
			d, err := waitForMode(c.g, "loader", 15*time.Second)
			if err != nil {
				return fmt.Errorf("download done, but %w", err)
			}
			fmt.Fprintf(c.out, "Download done. Device %d:%d is in Loader mode.\n", d.Bus, d.Dev)
			return nil
		},
	})

	var ulChip string
	var ulSector uint
	var ulVerify bool
	register(&command{
		name: "upgrade-loader", aliases: []string{"ul"}, group: "Device", args: "<loader.bin | idbloader.img>", nargs: [2]int{1, 1},
		summary: "Write the ID block (DDR init + miniloader/SPL) to storage",
		help: `Writes an ID block to sector 64 of eMMC/SD, where the boot ROM looks for it.
The input is either a loader file (its FlashData and FlashBoot entries are
packed into an ID block) or a ready ID block such as idbloader.img.
NAND storage is not supported.`,
		flags: func(fs *flag.FlagSet) {
			fs.StringVar(&ulChip, "chip", "", "chip for loader files, e.g. rk3128 (default: from the loader file)")
			fs.UintVar(&ulSector, "sector", rkimage.IDBSector, "target sector")
			fs.BoolVar(&ulVerify, "verify", true, "read back and compare")
		},
		run: func(c *cmdCtx) error {
			data, err := os.ReadFile(c.args[0])
			if err != nil {
				return err
			}
			idb, err := idbFromInput(data, ulChip)
			if err != nil {
				return err
			}
			if info, err := rkimage.ParseIDB(idb); err == nil {
				fmt.Fprintf(c.out, "ID block: %s, init %d sectors, init+boot %d sectors, %s\n",
					info.SPLHdr, info.InitSize, info.InitBootSize, humanBytes(uint64(len(idb))))
			}
			cl, closeFn, err := openLoader(c.g)
			if err != nil {
				return err
			}
			defer closeFn()
			return writeBuffer(c, cl, uint64(ulSector), idb, ulVerify, "loader")
		},
	})

	var infoCap bool
	register(&command{
		name: "info", group: "Device", nargs: [2]int{0, 0},
		summary: "Show flash ID, chip info, and storage size",
		flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&infoCap, "capability", false, "also read the capability field (newer loaders only)")
		},
		run: func(c *cmdCtx) error {
			cl, closeFn, err := openLoader(c.g)
			if err != nil {
				return err
			}
			defer closeFn()
			if err := printFlashID(c, cl); err != nil {
				return err
			}
			if err := printChipInfo(c, cl); err != nil {
				return err
			}
			if err := printFlashInfo(c, cl); err != nil {
				return err
			}
			if infoCap {
				return printCapability(c, cl)
			}
			return nil
		},
	})
	for _, s := range []struct {
		name, alias, summary string
		fn                   func(*cmdCtx, *rockusb.Client) error
	}{
		{"flash-id", "rid", "Read the flash ID", printFlashID},
		{"chip-info", "rci", "Read the chip info", printChipInfo},
		{"flash-info", "rfi", "Read the storage info", printFlashInfo},
		{"capability", "rcb", "Read the loader capability (newer loaders only)", printCapability},
	} {
		s := s
		register(&command{
			name: s.name, aliases: []string{s.alias}, group: "Device", nargs: [2]int{0, 0}, summary: s.summary,
			run: func(c *cmdCtx) error {
				cl, closeFn, err := openLoader(c.g)
				if err != nil {
					return err
				}
				defer closeFn()
				return s.fn(c, cl)
			},
		})
	}

	register(&command{
		name: "test", aliases: []string{"td"}, group: "Device", nargs: [2]int{0, 0},
		summary: "Check that the loader is ready",
		run: func(c *cmdCtx) error {
			cl, closeFn, err := openLoader(c.g)
			if err != nil {
				return err
			}
			defer closeFn()
			if err := cl.TestUnitReady(); err != nil {
				return err
			}
			fmt.Fprintln(c.out, "Device is ready.")
			return nil
		},
	})

	register(&command{
		name: "reset", aliases: []string{"rd"}, group: "Device", args: "[normal|msc|poweroff|maskrom|disconnect|N]", nargs: [2]int{0, 1},
		summary: "Reboot the device",
		run: func(c *cmdCtx) error {
			sub, err := resetSubcode(c.args)
			if err != nil {
				return err
			}
			cl, closeFn, err := openLoader(c.g)
			if err != nil {
				return err
			}
			defer closeFn()
			if err := cl.Reset(sub); err != nil {
				return err
			}
			fmt.Fprintln(c.out, "Reset sent.")
			return nil
		},
	})
}

func resetSubcode(args []string) (uint8, error) {
	if len(args) == 0 {
		return rockusb.ResetNormal, nil
	}
	m := map[string]uint8{"normal": rockusb.ResetNormal, "msc": rockusb.ResetMSC, "poweroff": rockusb.ResetPowerOff,
		"maskrom": rockusb.ResetMaskrom, "disconnect": rockusb.ResetDisconnect}
	if v, ok := m[strings.ToLower(args[0])]; ok {
		return v, nil
	}
	n, err := strconv.ParseUint(args[0], 0, 8)
	if err != nil {
		return 0, fmt.Errorf("unknown reset mode %q", args[0])
	}
	return uint8(n), nil
}

// chipForTag maps verified loader chip tags to ID block chips.
// Other chips need -chip.
var chipForTag = map[string]string{"312A": "rk3128"}

// idbFromInput accepts a loader file or a ready ID block.
func idbFromInput(data []byte, chipName string) ([]byte, error) {
	if _, err := rkimage.ParseIDB(data); err == nil {
		logx.Infof("input is an ID block, writing as is")
		return data, nil
	}
	bf, err := rkimage.ParseBootFile(data)
	if err != nil {
		return nil, fmt.Errorf("input is neither an ID block nor a loader file: %w", err)
	}
	if chipName == "" {
		chipName = chipForTag[bf.Chip()]
		if chipName == "" {
			return nil, fmt.Errorf("unknown loader chip %q; pass -chip", bf.Chip())
		}
	}
	chip, err := rkimage.FindChip(chipName)
	if err != nil {
		return nil, err
	}
	logx.Infof("building ID block for %s from loader file (chip tag %s)", chip.Name, bf.Chip())
	return rkimage.IDBFromBootFile(chip, bf)
}

func printFlashID(c *cmdCtx, cl *rockusb.Client) error {
	id, err := cl.FlashID()
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Flash ID:     % X  (%q)\n", id, strings.TrimSpace(string(id)))
	return nil
}

func printChipInfo(c *cmdCtx, cl *rockusb.Client) error {
	ci, err := cl.ChipInfo()
	if err != nil {
		return err
	}
	tag := rockusb.ChipTag(ci)
	fmt.Fprintf(c.out, "Chip info:    % X  (chip %q)\n", ci, tag)
	return nil
}

func printFlashInfo(c *cmdCtx, cl *rockusb.Client) error {
	fi, err := cl.FlashInfo()
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Flash size:   %d sectors (%s)\n", fi.Sectors, humanBytes(fi.Bytes()))
	fmt.Fprintf(c.out, "Block size:   %d KiB\n", fi.BlockSize/2)
	fmt.Fprintf(c.out, "Page size:    %d KiB\n", fi.PageSize/2)
	fmt.Fprintf(c.out, "ECC bits:     %d\n", fi.ECCBits)
	fmt.Fprintf(c.out, "Access time:  %d\n", fi.Access)
	fmt.Fprintf(c.out, "Manufacturer: 0x%02X\n", fi.Manuf)
	fmt.Fprintf(c.out, "Flash CS:     0x%02X\n", fi.CS)
	return nil
}

func printCapability(c *cmdCtx, cl *rockusb.Client) error {
	saved := cl.Timeout
	cl.Timeout = 3 * time.Second
	capab, err := cl.Capability()
	cl.Timeout = saved
	if err != nil {
		return fmt.Errorf("%w (old loaders do not support it; power-cycle the device if later commands fail)", err)
	}
	fmt.Fprintf(c.out, "Capability:   % X\n", capab)
	for _, b := range rockusb.CapabilityBits {
		state := "disabled"
		if int(b.Byte) < len(capab) && capab[b.Byte]&b.Mask != 0 {
			state = "enabled"
		}
		fmt.Fprintf(c.out, "  %-18s %s\n", b.Name+":", state)
	}
	return nil
}

// writeBuffer writes data at a sector, with optional read-back.
func writeBuffer(c *cmdCtx, cl *rockusb.Client, start uint64, data []byte, verify bool, label string) error {
	return writeStream(c, cl, start, bytes.NewReader(data), uint64(len(data)), verify, label)
}
