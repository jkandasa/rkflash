package rkimage

import (
	"encoding/binary"
	"fmt"
)

// ID block (idbloader) format v1, as written by U-Boot "mkimage -T rksd".
// The boot ROM reads it at sector 64 of SD/eMMC.
const (
	idbMagic      = 0x0ff0aa55
	idbInitOffset = 4 // sectors
	idbAlign      = 2048
	idbMaxBoot    = 512 << 10
	// Header0 fields after magic(4), reserved(4), disable_rc4(4),
	// init_offset(2), and reserved(492).
	idbInitSizeOff = 0x1fa
	idbInitBootOff = 0x1fc
	// IDBSector is the usual ID block location on SD/eMMC.
	IDBSector = 64
)

// Chip describes ID block parameters of one SoC (from U-Boot tools/rkcommon.c).
type Chip struct {
	Name    string
	SPLHdr  string // 4-byte tag written at the start of the init code
	SPLSize int    // SRAM limit of the init code
	RC4     bool   // RC4 per 512-byte block for init and boot code
}

// Chips lists SoCs with the v1 ID block format.
var Chips = []Chip{
	{"px30", "RK33", 0x2800, false},
	{"rk3036", "RK30", 0x1000, false},
	{"rk3066", "RK30", 0x8000 - 0x800, true},
	{"rk3128", "RK31", 0x1800, false},
	{"rk3188", "RK31", 0x8000 - 0x800, true},
	{"rk322x", "RK32", 0x8000 - 0x1000, false},
	{"rk3288", "RK32", 0x8000, false},
	{"rk3308", "RK33", 0x40000 - 0x1000, false},
	{"rk3328", "RK32", 0x8000 - 0x800, false},
	{"rk3368", "RK33", 0x8000 - 0x1000, false},
	{"rk3399", "RK33", 0x30000 - 0x2000, false},
	{"rv1108", "RK11", 0x1800, false},
	{"rv1126", "110B", 0x10000 - 0x1000, false},
}

// FindChip returns the chip parameters by name.
func FindChip(name string) (*Chip, error) {
	for i := range Chips {
		if Chips[i].Name == name {
			return &Chips[i], nil
		}
	}
	return nil, fmt.Errorf("unknown chip %q (see 'rkflash idb -h' for the list)", name)
}

// IDBOptions selects how the next stage follows the init code.
type IDBOptions struct {
	// Append places Boot right after the init code with
	// init_boot_size = init + 512 KiB, like "mkimage -d ddr" plus "cat spl".
	// Else Boot is padded to 2 KiB, like "mkimage -d ddr:spl".
	Append bool
}

// BuildIDB creates an ID block from init code (DDR init or TPL) and an
// optional boot stage (miniloader or SPL).
func BuildIDB(chip *Chip, initCode, boot []byte, opt IDBOptions) ([]byte, error) {
	initSize := alignUp(len(initCode), idbAlign)
	if initSize > chip.SPLSize {
		return nil, fmt.Errorf("init code is %d bytes, %s allows %d", initSize, chip.Name, chip.SPLSize)
	}
	bootSize := 0
	if len(boot) > 0 && !opt.Append {
		bootSize = alignUp(len(boot), idbAlign)
	}
	hdrLen := idbInitOffset * 512
	out := make([]byte, hdrLen+initSize+bootSize)
	copy(out[hdrLen:], initCode)
	if string(out[hdrLen:hdrLen+4]) != "RSAK" {
		copy(out[hdrLen:], chip.SPLHdr)
	}
	if bootSize > 0 {
		copy(out[hdrLen+initSize:], boot)
	}

	h := out[:512]
	binary.LittleEndian.PutUint32(h[0:], idbMagic)
	if !chip.RC4 {
		binary.LittleEndian.PutUint32(h[8:], 1) // disable_rc4
	}
	binary.LittleEndian.PutUint16(h[12:], idbInitOffset)
	binary.LittleEndian.PutUint16(h[idbInitSizeOff:], uint16(initSize/512))
	initBoot := initSize + idbMaxBoot
	if bootSize > 0 {
		initBoot = initSize + bootSize
	}
	binary.LittleEndian.PutUint16(h[idbInitBootOff:], uint16(initBoot/512))
	RC4(h)

	if chip.RC4 {
		RC4Blocks(out[hdrLen : hdrLen+initSize])
		if bootSize > 0 {
			RC4Blocks(out[hdrLen+initSize:])
		}
	}
	if len(boot) > 0 && opt.Append {
		out = append(out, boot...)
	}
	return out, nil
}

// IDBInfo describes a parsed ID block header.
type IDBInfo struct {
	DisableRC4   bool
	InitOffset   int // sectors
	InitSize     int // sectors
	InitBootSize int // sectors
	SPLHdr       string
}

// ParseIDB decodes the header of an ID block (v1).
func ParseIDB(d []byte) (*IDBInfo, error) {
	if len(d) < 512 {
		return nil, fmt.Errorf("too short for an ID block")
	}
	h := append([]byte(nil), d[:512]...)
	RC4(h)
	if binary.LittleEndian.Uint32(h) != idbMagic {
		return nil, fmt.Errorf("no ID block magic")
	}
	info := &IDBInfo{
		DisableRC4:   binary.LittleEndian.Uint32(h[8:]) != 0,
		InitOffset:   int(binary.LittleEndian.Uint16(h[12:])),
		InitSize:     int(binary.LittleEndian.Uint16(h[idbInitSizeOff:])),
		InitBootSize: int(binary.LittleEndian.Uint16(h[idbInitBootOff:])),
	}
	if off := info.InitOffset * 512; off+4 <= len(d) {
		tag := append([]byte(nil), d[off:off+4]...)
		if !info.DisableRC4 {
			blk := append([]byte(nil), d[off:min(off+512, len(d))]...)
			RC4(blk)
			tag = blk[:4]
		}
		info.SPLHdr = string(tag)
	}
	return info, nil
}

// IDBFromBootFile builds an ID block from the FlashData (DDR init) and
// FlashBoot (miniloader) entries of a loader file, like "rkdeveloptool ul".
func IDBFromBootFile(chip *Chip, bf *BootFile) ([]byte, error) {
	fd := bf.FindLoader("FlashData")
	fb := bf.FindLoader("FlashBoot")
	if fd == nil || fb == nil {
		return nil, fmt.Errorf("loader file has no FlashData/FlashBoot entries")
	}
	return BuildIDB(chip, fd.Data, fb.Data, IDBOptions{Append: true})
}
