package rkimage

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// LOADER/TOS image layout, as produced by Rockchip "loaderimage".
// The miniloader loads U-Boot ("LOADER") and the trust OS ("TOS") from these.
const (
	loaderHdrSize = 0x800
	loaderMagicU  = "LOADER  "
	loaderMagicT  = "TOS     "
)

// LoaderImage describes one LOADER/TOS image copy.
type LoaderImage struct {
	Magic    string
	Version  uint32
	LoadAddr uint32
	Size     uint32
	CRC      uint32
	HashLen  uint32
	Hash     []byte
	Copies   int
	Signed   bool // RSA "SIGN" block at 0x400
	CRCOK    bool
	HashOK   bool
}

// loaderHash hashes data, then version (if nonzero), load address, size, and hash length.
func loaderHash(data []byte, version, addr, size, hashLen uint32) []byte {
	var tail []byte
	if version > 0 {
		tail = binary.LittleEndian.AppendUint32(tail, version)
	}
	tail = binary.LittleEndian.AppendUint32(tail, addr)
	tail = binary.LittleEndian.AppendUint32(tail, size)
	tail = binary.LittleEndian.AppendUint32(tail, hashLen)
	switch hashLen {
	case 32:
		h := sha256.New()
		h.Write(data)
		h.Write(tail)
		return h.Sum(nil)
	case 20:
		h := sha1.New()
		h.Write(data)
		h.Write(tail)
		return h.Sum(nil)
	}
	return nil
}

// ParseLoaderImage decodes and verifies a LOADER/TOS image.
func ParseLoaderImage(d []byte) (*LoaderImage, error) {
	if len(d) < loaderHdrSize {
		return nil, fmt.Errorf("too short for a LOADER/TOS image")
	}
	magic := string(d[:8])
	if magic != loaderMagicU && magic != loaderMagicT {
		return nil, fmt.Errorf("no LOADER/TOS magic")
	}
	li := &LoaderImage{
		Magic:    magic,
		Version:  binary.LittleEndian.Uint32(d[8:]),
		LoadAddr: binary.LittleEndian.Uint32(d[16:]),
		Size:     binary.LittleEndian.Uint32(d[20:]),
		CRC:      binary.LittleEndian.Uint32(d[24:]),
		HashLen:  binary.LittleEndian.Uint32(d[28:]),
	}
	if li.HashLen > 32 {
		return nil, fmt.Errorf("bad hash length %d", li.HashLen)
	}
	li.Hash = append([]byte(nil), d[32:32+li.HashLen]...)
	li.Signed = string(d[0x400:0x404]) == "SIGN"
	end := loaderHdrSize + int(li.Size)
	if end > len(d) {
		return nil, fmt.Errorf("image size %d exceeds file", li.Size)
	}
	data := d[loaderHdrSize:end]
	li.CRCOK = RKCRC32(data) == li.CRC
	if h := loaderHash(data, li.Version, li.LoadAddr, li.Size, li.HashLen); h != nil {
		li.HashOK = string(h[:li.HashLen]) == string(li.Hash)
	}
	for off := 0; off+8 <= len(d); off += 1 << 20 {
		if string(d[off:off+8]) == magic {
			li.Copies++
		}
	}
	return li, nil
}

// LoaderImageOptions controls PackLoaderImage.
type LoaderImageOptions struct {
	Magic    string // "LOADER" (default) or "TOS"
	LoadAddr uint32
	HashLen  int // 32 (SHA-256, default), 20 (SHA-1), or 0
	CopySize int // bytes per copy, default 1 MiB
	Copies   int // default 4
}

// PackLoaderImage wraps a binary (e.g. u-boot-dtb.bin) like "loaderimage --pack".
func PackLoaderImage(data []byte, opt LoaderImageOptions) ([]byte, error) {
	magic := loaderMagicU
	if opt.Magic == "TOS" {
		magic = loaderMagicT
	} else if opt.Magic != "" && opt.Magic != "LOADER" {
		return nil, fmt.Errorf("magic must be LOADER or TOS")
	}
	if opt.HashLen != 0 && opt.HashLen != 20 && opt.HashLen != 32 {
		return nil, fmt.Errorf("hash length must be 0, 20, or 32")
	}
	if opt.CopySize == 0 {
		opt.CopySize = 1 << 20
	}
	if opt.Copies == 0 {
		opt.Copies = 4
	}
	if loaderHdrSize+len(data) > opt.CopySize {
		return nil, fmt.Errorf("%d bytes do not fit in a %d-byte copy", len(data), opt.CopySize)
	}
	one := make([]byte, opt.CopySize)
	copy(one, magic)
	size := uint32(len(data))
	binary.LittleEndian.PutUint32(one[16:], opt.LoadAddr)
	binary.LittleEndian.PutUint32(one[20:], size)
	binary.LittleEndian.PutUint32(one[24:], RKCRC32(data))
	binary.LittleEndian.PutUint32(one[28:], uint32(opt.HashLen))
	if opt.HashLen > 0 {
		copy(one[32:], loaderHash(data, 0, opt.LoadAddr, size, uint32(opt.HashLen)))
	}
	copy(one[loaderHdrSize:], data)
	out := make([]byte, 0, opt.CopySize*opt.Copies)
	for i := 0; i < opt.Copies; i++ {
		out = append(out, one...)
	}
	return out, nil
}

// UnpackLoaderImage returns the payload of a LOADER/TOS image.
func UnpackLoaderImage(d []byte) ([]byte, *LoaderImage, error) {
	li, err := ParseLoaderImage(d)
	if err != nil {
		return nil, nil, err
	}
	return append([]byte(nil), d[loaderHdrSize:loaderHdrSize+int(li.Size)]...), li, nil
}
