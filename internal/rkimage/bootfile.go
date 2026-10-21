package rkimage

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
	"unicode/utf16"
)

// Loader file ("BOOT") layout, as produced by Rockchip boot_merger.
const (
	bootHeaderSize = 102
	bootEntrySize  = 57
	bootAlign      = 2048
)

// Entry types.
const (
	Entry471    = 1
	Entry472    = 2
	EntryLoader = 4
)

// BootEntry is one binary inside a loader file. Data is plaintext.
type BootEntry struct {
	Type  uint32
	Name  string
	Delay uint32 // ms, after a 0x471/0x472 download
	Data  []byte
}

// BootFile is a parsed loader file.
type BootFile struct {
	Tag           string // "BOOT" or "LDR "
	Version       uint32
	MergerVersion uint32
	Time          time.Time
	ChipType      uint32
	SignFlag      uint8
	RC4Flag       uint8
	Entries471    []BootEntry
	Entries472    []BootEntry
	EntriesLoader []BootEntry
}

// Chip returns the chip tag, e.g. "312A".
func (b *BootFile) Chip() string {
	var c [4]byte
	binary.BigEndian.PutUint32(c[:], b.ChipType)
	return string(c[:])
}

// FindLoader returns a loader entry by name, e.g. "FlashData" or "FlashBoot".
func (b *BootFile) FindLoader(name string) *BootEntry {
	for i := range b.EntriesLoader {
		if b.EntriesLoader[i].Name == name {
			return &b.EntriesLoader[i]
		}
	}
	return nil
}

// entryCipher: 0x471/0x472 are one RC4 stream. Flash entries are RC4 per
// 512-byte block, as the ID block format wants.
func decryptEntry(typ uint32, d []byte) {
	if typ == EntryLoader {
		RC4Blocks(d)
	} else {
		RC4(d)
	}
}

// ParseBootFile decodes a loader file and decrypts its entries.
func ParseBootFile(d []byte) (*BootFile, error) {
	if len(d) < bootHeaderSize+4 || (string(d[:4]) != "BOOT" && string(d[:4]) != "LDR ") {
		return nil, fmt.Errorf("not a Rockchip loader file")
	}
	if want, got := binary.LittleEndian.Uint32(d[len(d)-4:]), RKCRC32(d[:len(d)-4]); want != got {
		return nil, fmt.Errorf("loader file CRC mismatch (stored %08x, computed %08x)", want, got)
	}
	bf := &BootFile{
		Tag:           string(d[:4]),
		Version:       binary.LittleEndian.Uint32(d[6:]),
		MergerVersion: binary.LittleEndian.Uint32(d[10:]),
		ChipType:      binary.LittleEndian.Uint32(d[21:]),
		SignFlag:      d[43],
		RC4Flag:       d[44],
	}
	bf.Time = time.Date(int(binary.LittleEndian.Uint16(d[14:])), time.Month(d[16]), int(d[17]),
		int(d[18]), int(d[19]), int(d[20]), 0, time.UTC)
	parse := func(num uint8, off uint32, esize uint8) ([]BootEntry, error) {
		var out []BootEntry
		for i := 0; i < int(num); i++ {
			s := int(off) + i*int(esize)
			if esize < bootEntrySize || s+bootEntrySize > len(d) {
				return nil, fmt.Errorf("entry table outside file")
			}
			e := d[s : s+bootEntrySize]
			u := make([]uint16, 20)
			for k := range u {
				u[k] = binary.LittleEndian.Uint16(e[5+2*k:])
			}
			typ := binary.LittleEndian.Uint32(e[1:])
			dOff := binary.LittleEndian.Uint32(e[45:])
			dSize := binary.LittleEndian.Uint32(e[49:])
			if uint64(dOff)+uint64(dSize) > uint64(len(d)) {
				return nil, fmt.Errorf("entry data outside file")
			}
			data := append([]byte(nil), d[dOff:dOff+dSize]...)
			decryptEntry(typ, data)
			out = append(out, BootEntry{
				Type:  typ,
				Name:  strings.TrimRight(string(utf16.Decode(u)), "\x00"),
				Delay: binary.LittleEndian.Uint32(e[53:]),
				Data:  data,
			})
		}
		return out, nil
	}
	var err error
	if bf.Entries471, err = parse(d[25], binary.LittleEndian.Uint32(d[26:]), d[30]); err != nil {
		return nil, err
	}
	if bf.Entries472, err = parse(d[31], binary.LittleEndian.Uint32(d[32:]), d[36]); err != nil {
		return nil, err
	}
	if bf.EntriesLoader, err = parse(d[37], binary.LittleEndian.Uint32(d[38:]), d[42]); err != nil {
		return nil, err
	}
	return bf, nil
}

func alignUp(n, a int) int { return (n + a - 1) / a * a }

// Marshal encodes the loader file. Entry data is padded to 2 KiB and encrypted.
func (b *BootFile) Marshal() ([]byte, error) {
	groups := [][]BootEntry{b.Entries471, b.Entries472, b.EntriesLoader}
	n := 0
	for _, g := range groups {
		if len(g) > 255 {
			return nil, fmt.Errorf("too many entries")
		}
		n += len(g)
	}
	var buf bytes.Buffer
	hdr := make([]byte, bootHeaderSize)
	tag := b.Tag
	if tag == "" {
		tag = "BOOT"
	}
	copy(hdr, tag)
	binary.LittleEndian.PutUint16(hdr[4:], bootHeaderSize)
	binary.LittleEndian.PutUint32(hdr[6:], b.Version)
	binary.LittleEndian.PutUint32(hdr[10:], b.MergerVersion)
	t := b.Time
	if t.IsZero() {
		t = time.Now().UTC()
	}
	binary.LittleEndian.PutUint16(hdr[14:], uint16(t.Year()))
	hdr[16], hdr[17], hdr[18], hdr[19], hdr[20] = byte(t.Month()), byte(t.Day()), byte(t.Hour()), byte(t.Minute()), byte(t.Second())
	binary.LittleEndian.PutUint32(hdr[21:], b.ChipType)
	tableOff := uint32(bootHeaderSize)
	for i, g := range groups {
		base := 25 + i*6
		hdr[base] = byte(len(g))
		binary.LittleEndian.PutUint32(hdr[base+1:], tableOff)
		hdr[base+5] = bootEntrySize
		tableOff += uint32(len(g) * bootEntrySize)
	}
	hdr[43] = b.SignFlag
	hdr[44] = b.RC4Flag
	buf.Write(hdr)

	dataOff := uint32(bootHeaderSize + n*bootEntrySize)
	var blobs [][]byte
	for _, g := range groups {
		for _, e := range g {
			data := make([]byte, alignUp(len(e.Data), bootAlign))
			copy(data, e.Data)
			decryptEntry(e.Type, data) // RC4 is symmetric
			ent := make([]byte, bootEntrySize)
			ent[0] = bootEntrySize
			binary.LittleEndian.PutUint32(ent[1:], e.Type)
			name := utf16.Encode([]rune(e.Name))
			if len(name) > 19 { // keep a terminating NUL
				name = name[:19]
			}
			for k, r := range name {
				binary.LittleEndian.PutUint16(ent[5+2*k:], r)
			}
			binary.LittleEndian.PutUint32(ent[45:], dataOff)
			binary.LittleEndian.PutUint32(ent[49:], uint32(len(data)))
			binary.LittleEndian.PutUint32(ent[53:], e.Delay)
			buf.Write(ent)
			blobs = append(blobs, data)
			dataOff += uint32(len(data))
		}
	}
	for _, d := range blobs {
		buf.Write(d)
	}
	out := buf.Bytes()
	crc := make([]byte, 4)
	binary.LittleEndian.PutUint32(crc, RKCRC32(out))
	return append(out, crc...), nil
}

// ChipTypeFromTag converts "312A" to the header field value.
func ChipTypeFromTag(tag string) (uint32, error) {
	if len(tag) != 4 {
		return 0, fmt.Errorf("chip tag %q must have 4 characters", tag)
	}
	return binary.BigEndian.Uint32([]byte(tag)), nil
}
