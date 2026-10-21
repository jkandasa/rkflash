// Package part reads and writes partition tables on a sector device:
// GPT, MBR, and the Rockchip parameter (PARM) block.
package part

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"strings"
	"unicode/utf16"

	"github.com/jkandasa/rkflash/internal/rkimage"
)

// SectorSize is the logical sector size.
const SectorSize = 512

// Partition is one entry of any table type. Values are in sectors.
type Partition struct {
	Index int
	Name  string
	Start uint64
	Size  uint64
	Type  string
	UUID  string
}

// End returns the last sector (inclusive).
func (p Partition) End() uint64 { return p.Start + p.Size - 1 }

// Table is a detected partition table.
type Table struct {
	Kind       string // "gpt", "mbr", or "parameter"
	DiskGUID   string
	Partitions []Partition
	Parameter  string // parameter text for kind "parameter"
}

// Find returns a partition by name (case-insensitive).
func (t *Table) Find(name string) (*Partition, error) {
	for i := range t.Partitions {
		if strings.EqualFold(t.Partitions[i].Name, name) {
			return &t.Partitions[i], nil
		}
	}
	var names []string
	for _, p := range t.Partitions {
		names = append(names, p.Name)
	}
	return nil, fmt.Errorf("partition %q not found (have: %s)", name, strings.Join(names, ", "))
}

// GUID helpers. GPT stores the first three fields little-endian.

// FormatGUID renders a GPT on-disk GUID.
func FormatGUID(b []byte) string {
	return fmt.Sprintf("%08X-%04X-%04X-%02X%02X-%X",
		binary.LittleEndian.Uint32(b[0:]), binary.LittleEndian.Uint16(b[4:]),
		binary.LittleEndian.Uint16(b[6:]), b[8], b[9], b[10:16])
}

// ParseGUID converts "XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX" to on-disk bytes.
func ParseGUID(s string) ([]byte, error) {
	h := strings.ReplaceAll(s, "-", "")
	if len(h) != 32 {
		return nil, fmt.Errorf("bad GUID %q", s)
	}
	var raw [16]byte
	for i := 0; i < 16; i++ {
		var v byte
		if _, err := fmt.Sscanf(h[2*i:2*i+2], "%02x", &v); err != nil {
			return nil, fmt.Errorf("bad GUID %q", s)
		}
		raw[i] = v
	}
	out := make([]byte, 16)
	binary.LittleEndian.PutUint32(out[0:], binary.BigEndian.Uint32(raw[0:]))
	binary.LittleEndian.PutUint16(out[4:], binary.BigEndian.Uint16(raw[4:]))
	binary.LittleEndian.PutUint16(out[6:], binary.BigEndian.Uint16(raw[6:]))
	copy(out[8:], raw[8:])
	return out, nil
}

// RandomGUID returns a random version-4 GUID in on-disk byte order.
func RandomGUID() []byte {
	var raw [16]byte
	rand.Read(raw[:])
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	g, _ := ParseGUID(fmt.Sprintf("%X-%X-%X-%X-%X", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]))
	return g
}

// LinuxDataGUID is the GPT type of Linux filesystem data.
const LinuxDataGUID = "0FC63DAF-8483-4772-8E79-3D69D8477DE4"

var mbrTypes = map[byte]string{0x83: "Linux", 0x0c: "FAT32 LBA", 0x0b: "FAT32", 0x07: "NTFS/exFAT",
	0x82: "Linux swap", 0xee: "GPT protective", 0xef: "EFI", 0x06: "FAT16"}

// Detect parses the first sectors of a device (at least 34 sectors).
// It tries GPT, then MBR, then the parameter block.
func Detect(head []byte) (*Table, error) {
	if len(head) < 34*SectorSize {
		return nil, fmt.Errorf("need 34 sectors")
	}
	if t, err := parseGPT(head); err == nil {
		return t, nil
	}
	if string(head[:4]) == "PARM" {
		text, err := rkimage.DecodeParmBlock(head)
		if err != nil {
			return nil, err
		}
		p, err := rkimage.ParseParameter(text)
		if err != nil {
			return nil, err
		}
		t := &Table{Kind: "parameter", Parameter: text}
		for i, mp := range p.Parts {
			t.Partitions = append(t.Partitions, Partition{Index: i + 1, Name: mp.Name, Start: mp.Offset, Size: mp.Size})
		}
		return t, nil
	}
	if head[510] == 0x55 && head[511] == 0xaa {
		t := &Table{Kind: "mbr"}
		for i := 0; i < 4; i++ {
			e := head[446+16*i:]
			typ := e[4]
			start := uint64(binary.LittleEndian.Uint32(e[8:]))
			size := uint64(binary.LittleEndian.Uint32(e[12:]))
			if typ == 0 || size == 0 {
				continue
			}
			name := mbrTypes[typ]
			if name == "" {
				name = fmt.Sprintf("type 0x%02x", typ)
			}
			t.Partitions = append(t.Partitions, Partition{Index: i + 1, Name: fmt.Sprintf("p%d", i+1),
				Start: start, Size: size, Type: name})
		}
		return t, nil
	}
	return nil, fmt.Errorf("no GPT, MBR, or parameter found")
}

func parseGPT(head []byte) (*Table, error) {
	h := head[SectorSize : 2*SectorSize]
	if string(h[:8]) != "EFI PART" {
		return nil, fmt.Errorf("no GPT signature")
	}
	hsize := binary.LittleEndian.Uint32(h[12:])
	if hsize < 92 || hsize > SectorSize {
		return nil, fmt.Errorf("bad GPT header size")
	}
	hdr := append([]byte(nil), h[:hsize]...)
	stored := binary.LittleEndian.Uint32(hdr[16:])
	binary.LittleEndian.PutUint32(hdr[16:], 0)
	if crc32.ChecksumIEEE(hdr) != stored {
		return nil, fmt.Errorf("GPT header CRC mismatch")
	}
	entLBA := binary.LittleEndian.Uint64(h[72:])
	num := binary.LittleEndian.Uint32(h[80:])
	esz := binary.LittleEndian.Uint32(h[84:])
	need := entLBA*SectorSize + uint64(num)*uint64(esz)
	if esz < 128 || need > uint64(len(head)) {
		return nil, fmt.Errorf("GPT entries outside the read area")
	}
	ents := head[entLBA*SectorSize : need]
	if crc32.ChecksumIEEE(ents) != binary.LittleEndian.Uint32(h[88:]) {
		return nil, fmt.Errorf("GPT entries CRC mismatch")
	}
	t := &Table{Kind: "gpt", DiskGUID: FormatGUID(h[56:72])}
	for i := 0; i < int(num); i++ {
		e := ents[i*int(esz) : (i+1)*int(esz)]
		if allZero(e[:16]) {
			continue
		}
		first := binary.LittleEndian.Uint64(e[32:])
		last := binary.LittleEndian.Uint64(e[40:])
		u := make([]uint16, 36)
		for k := range u {
			u[k] = binary.LittleEndian.Uint16(e[56+2*k:])
		}
		t.Partitions = append(t.Partitions, Partition{
			Index: i + 1, Name: strings.TrimRight(string(utf16.Decode(u)), "\x00"),
			Start: first, Size: last - first + 1, Type: FormatGUID(e[0:16]), UUID: FormatGUID(e[16:32]),
		})
	}
	return t, nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// GPTEntry is one partition to place in a new GPT.
type GPTEntry struct {
	Name  string
	Start uint64
	Size  uint64 // 0 = grow to the last usable sector
	UUID  string // optional
	Type  string // optional, default Linux data
}

// GPTImage holds the sectors of a new GPT.
type GPTImage struct {
	Primary    []byte // sectors 0..33: protective MBR, header, entries
	Backup     []byte // last 33 sectors: entries, header
	BackupLBA  uint64 // first sector of Backup
	Partitions []Partition
}

// BuildGPT creates a GPT for a disk of diskSectors sectors.
func BuildGPT(diskSectors uint64, entries []GPTEntry) (*GPTImage, error) {
	const numEnt, entSize = 128, 128
	entSectors := uint64(numEnt * entSize / SectorSize) // 32
	firstUsable := 2 + entSectors                       // 34
	lastUsable := diskSectors - entSectors - 2
	if diskSectors < 2*firstUsable {
		return nil, fmt.Errorf("disk too small")
	}
	if len(entries) > numEnt {
		return nil, fmt.Errorf("too many partitions")
	}
	ents := make([]byte, numEnt*entSize)
	var parts []Partition
	for i, e := range entries {
		if e.Start < firstUsable {
			return nil, fmt.Errorf("partition %q starts at %d, before first usable sector %d", e.Name, e.Start, firstUsable)
		}
		last := e.Start + e.Size - 1
		if e.Size == 0 {
			last = lastUsable
		}
		if last > lastUsable || last < e.Start {
			return nil, fmt.Errorf("partition %q (%d+%d) does not fit the disk (last usable %d)", e.Name, e.Start, e.Size, lastUsable)
		}
		if i > 0 && e.Start <= parts[i-1].End() {
			return nil, fmt.Errorf("partition %q overlaps %q", e.Name, parts[i-1].Name)
		}
		typ := e.Type
		if typ == "" {
			typ = LinuxDataGUID
		}
		tg, err := ParseGUID(typ)
		if err != nil {
			return nil, err
		}
		ug := RandomGUID()
		if e.UUID != "" {
			if ug, err = ParseGUID(e.UUID); err != nil {
				return nil, err
			}
		}
		b := ents[i*entSize:]
		copy(b[0:], tg)
		copy(b[16:], ug)
		binary.LittleEndian.PutUint64(b[32:], e.Start)
		binary.LittleEndian.PutUint64(b[40:], last)
		name := utf16.Encode([]rune(e.Name))
		if len(name) > 36 {
			return nil, fmt.Errorf("partition name %q is longer than 36 characters", e.Name)
		}
		for k, r := range name {
			binary.LittleEndian.PutUint16(b[56+2*k:], r)
		}
		parts = append(parts, Partition{Index: i + 1, Name: e.Name, Start: e.Start, Size: last - e.Start + 1,
			Type: FormatGUID(tg), UUID: FormatGUID(ug)})
	}
	entCRC := crc32.ChecksumIEEE(ents)
	diskGUID := RandomGUID()
	header := func(my, alt, entLBA uint64) []byte {
		h := make([]byte, SectorSize)
		copy(h, "EFI PART")
		binary.LittleEndian.PutUint32(h[8:], 0x00010000)
		binary.LittleEndian.PutUint32(h[12:], 92)
		binary.LittleEndian.PutUint64(h[24:], my)
		binary.LittleEndian.PutUint64(h[32:], alt)
		binary.LittleEndian.PutUint64(h[40:], firstUsable)
		binary.LittleEndian.PutUint64(h[48:], lastUsable)
		copy(h[56:], diskGUID)
		binary.LittleEndian.PutUint64(h[72:], entLBA)
		binary.LittleEndian.PutUint32(h[80:], numEnt)
		binary.LittleEndian.PutUint32(h[84:], entSize)
		binary.LittleEndian.PutUint32(h[88:], entCRC)
		binary.LittleEndian.PutUint32(h[16:], crc32.ChecksumIEEE(h[:92]))
		return h
	}
	last := diskSectors - 1
	img := &GPTImage{Partitions: parts, BackupLBA: last - entSectors}
	mbr := make([]byte, SectorSize)
	pe := mbr[446:]
	pe[2] = 0x02 // CHS of sector 1
	pe[4] = 0xee
	pe[5], pe[6], pe[7] = 0xff, 0xff, 0xff
	binary.LittleEndian.PutUint32(pe[8:], 1)
	size := diskSectors - 1
	if size > 0xffffffff {
		size = 0xffffffff
	}
	binary.LittleEndian.PutUint32(pe[12:], uint32(size))
	mbr[510], mbr[511] = 0x55, 0xaa
	img.Primary = append(append(mbr, header(1, last, 2)...), ents...)
	img.Backup = append(append([]byte(nil), ents...), header(last, 1, last-entSectors)...)
	return img, nil
}

// GPTFromParameter converts parameter mtdparts to GPT entries.
func GPTFromParameter(p *rkimage.Parameter) []GPTEntry {
	var out []GPTEntry
	for _, mp := range p.Parts {
		e := GPTEntry{Name: mp.Name, Start: mp.Offset, UUID: p.UUIDs[mp.Name]}
		if !mp.Grow {
			e.Size = mp.Size
		}
		out = append(out, e)
	}
	return out
}
