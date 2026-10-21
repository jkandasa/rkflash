package rkimage

import (
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Rockchip parameter ("PARM") block. Legacy firmware stores it at sector 0
// with copies every 1024 sectors. It holds the mtdparts partition table.
const (
	parmMagic = "PARM"
	// ParmCopies and ParmStride match the factory layout.
	ParmCopies = 8
	ParmStride = 0x400 // sectors
)

// Parameter is a parsed parameter text.
type Parameter struct {
	Text  string
	Lines map[string]string // KEY -> value ("CMDLINE", "MACHINE_MODEL", ...)
	Parts []MtdPart
	UUIDs map[string]string // "uuid:name=GUID" lines
}

// MtdPart is one mtdparts entry. Values are in 512-byte sectors.
type MtdPart struct {
	Name   string
	Offset uint64
	Size   uint64 // 0 means "grow to the end"
	Grow   bool
}

var mtdRe = regexp.MustCompile(`(-|0x[0-9a-fA-F]+)@(0x[0-9a-fA-F]+)\(([^)]+)\)`)

// ParseParameter parses parameter text (parameter.txt).
func ParseParameter(text string) (*Parameter, error) {
	p := &Parameter{Text: text, Lines: map[string]string{}, UUIDs: map[string]string{}}
	for _, ln := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(ln), "uuid:") {
			kv := strings.SplitN(ln[5:], "=", 2)
			if len(kv) == 2 {
				p.UUIDs[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
			}
			continue
		}
		kv := strings.SplitN(ln, ":", 2)
		if len(kv) == 2 {
			p.Lines[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	cmd := p.Lines["CMDLINE"]
	i := strings.Index(cmd, "mtdparts=")
	if i < 0 {
		return p, fmt.Errorf("parameter has no mtdparts in CMDLINE")
	}
	spec := cmd[i+len("mtdparts="):]
	if j := strings.IndexAny(spec, " \t"); j >= 0 {
		spec = spec[:j]
	}
	if j := strings.Index(spec, ":"); j >= 0 {
		spec = spec[j+1:]
	}
	for _, m := range mtdRe.FindAllStringSubmatch(spec, -1) {
		off, _ := strconv.ParseUint(m[2], 0, 64)
		part := MtdPart{Offset: off}
		name := m[3]
		if strings.HasSuffix(name, ":grow") {
			name, part.Grow = strings.TrimSuffix(name, ":grow"), true
		}
		part.Name = name
		if m[1] == "-" {
			part.Grow = true
		} else {
			part.Size, _ = strconv.ParseUint(m[1], 0, 64)
		}
		p.Parts = append(p.Parts, part)
	}
	if len(p.Parts) == 0 {
		return p, fmt.Errorf("mtdparts has no partitions")
	}
	return p, nil
}

// EncodeParmBlock wraps parameter text: "PARM", length, text, RK CRC32.
func EncodeParmBlock(text string) []byte {
	b := make([]byte, 8+len(text)+4)
	copy(b, parmMagic)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(text)))
	copy(b[8:], text)
	binary.LittleEndian.PutUint32(b[8+len(text):], RKCRC32([]byte(text)))
	return b
}

// DecodeParmBlock extracts the text of a PARM block and checks its CRC.
func DecodeParmBlock(d []byte) (string, error) {
	if len(d) < 12 || string(d[:4]) != parmMagic {
		return "", fmt.Errorf("no PARM magic")
	}
	n := binary.LittleEndian.Uint32(d[4:])
	if int(n)+12 > len(d) {
		return "", fmt.Errorf("PARM length %d exceeds data", n)
	}
	text := d[8 : 8+n]
	if want, got := binary.LittleEndian.Uint32(d[8+n:]), RKCRC32(text); want != got {
		return string(text), fmt.Errorf("PARM CRC mismatch (stored %08x, computed %08x)", want, got)
	}
	return string(text), nil
}
