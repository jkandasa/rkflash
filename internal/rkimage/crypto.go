// Package rkimage reads and writes Rockchip image formats:
// loader files (BOOT), ID blocks (idbloader), LOADER/TOS images, and parameters.
package rkimage

// rc4Key is the fixed key of the Rockchip boot ROM.
var rc4Key = []byte{124, 78, 3, 4, 85, 5, 9, 7, 45, 44, 123, 56, 23, 13, 23, 17}

// RC4 encrypts or decrypts data in place as one stream.
func RC4(data []byte) {
	var s [256]byte
	for i := range s {
		s[i] = byte(i)
	}
	j := 0
	for i := 0; i < 256; i++ {
		j = (j + int(s[i]) + int(rc4Key[i%len(rc4Key)])) & 255
		s[i], s[j] = s[j], s[i]
	}
	i, k := 0, 0
	for n := range data {
		i = (i + 1) & 255
		k = (k + int(s[i])) & 255
		s[i], s[k] = s[k], s[i]
		data[n] ^= s[(int(s[i])+int(s[k]))&255]
	}
}

// RC4Blocks applies RC4 to each 512-byte block with a fresh state.
func RC4Blocks(data []byte) {
	for off := 0; off < len(data); off += 512 {
		RC4(data[off:min(off+512, len(data))])
	}
}

// CRC16CCITT matches rkdeveloptool CRC_CCITT (bitwise, init 0xffff).
func CRC16CCITT(p []byte) uint16 {
	crc := uint16(0xffff)
	for _, b := range p {
		for mask := byte(0x80); mask != 0; mask >>= 1 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
			if b&mask != 0 {
				crc ^= 0x1021
			}
		}
	}
	return crc
}

var rkcrcTable = func() (t [256]uint32) {
	for i := range t {
		c := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04c10db7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return
}()

// RKCRC32 is the Rockchip CRC32 (poly 0x04C10DB7, init 0, MSB first).
// Loader files, LOADER/TOS images, and parameter blocks use it.
func RKCRC32(p []byte) uint32 {
	var crc uint32
	for _, b := range p {
		crc = crc<<8 ^ rkcrcTable[byte(crc>>24)^b]
	}
	return crc
}
