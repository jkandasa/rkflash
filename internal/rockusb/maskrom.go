package rockusb

import (
	"fmt"
	"time"

	"github.com/jkandasa/rkflash/internal/logx"
	"github.com/jkandasa/rkflash/internal/rkimage"
)

// Control is the control pipe to a device in Maskrom mode.
type Control interface {
	ControlOut(reqType, req uint8, value, index uint16, data []byte, timeout time.Duration) (int, error)
}

// Maskrom request codes (wIndex).
const (
	Req471 = 0x471 // DDR init, runs in SRAM
	Req472 = 0x472 // USB plug, runs in DRAM
)

const maskromChunk = 4096

// MaskromPayload frames data as rkdeveloptool RKU_DeviceRequest does:
// pad sizes 4095 mod 4096, then append a big-endian CRC-CCITT.
// It also reports whether a trailing 1-byte packet is needed.
func MaskromPayload(payload []byte) (data []byte, pad bool) {
	data = make([]byte, len(payload), len(payload)+5)
	copy(data, payload)
	switch len(data) % maskromChunk {
	case maskromChunk - 1:
		data = append(data, 0)
	case maskromChunk - 2:
		pad = true
	}
	crc := rkimage.CRC16CCITT(data)
	return append(data, byte(crc>>8), byte(crc)), pad
}

// DeviceRequest sends one 0x471 or 0x472 payload.
func DeviceRequest(c Control, code uint16, payload []byte) error {
	data, pad := MaskromPayload(payload)
	logx.Debugf("maskrom: request 0x%x, %d bytes (+crc), %d chunks", code, len(payload), (len(data)+maskromChunk-1)/maskromChunk)
	for off := 0; off < len(data); off += maskromChunk {
		end := min(off+maskromChunk, len(data))
		n, err := c.ControlOut(0x40, 0x0c, 0, code, data[off:end], 5*time.Second)
		if err != nil {
			return fmt.Errorf("maskrom request 0x%x at offset %d: %w", code, off, err)
		}
		if n != end-off {
			return fmt.Errorf("maskrom request 0x%x: short transfer %d/%d", code, n, end-off)
		}
	}
	if pad {
		if _, err := c.ControlOut(0x40, 0x0c, 0, code, []byte{0}, 5*time.Second); err != nil {
			return fmt.Errorf("maskrom request 0x%x: pad packet: %w", code, err)
		}
	}
	return nil
}

// DownloadBoot sends the 0x471 and 0x472 entries of a loader file.
// The device then starts the USB plug and re-enumerates in Loader mode.
func DownloadBoot(c Control, bf *rkimage.BootFile, report func(e *rkimage.BootEntry)) error {
	for _, group := range []struct {
		code    uint16
		entries []rkimage.BootEntry
	}{{Req471, bf.Entries471}, {Req472, bf.Entries472}} {
		for i := range group.entries {
			e := &group.entries[i]
			if report != nil {
				report(e)
			}
			if err := DeviceRequest(c, group.code, e.Data); err != nil {
				return fmt.Errorf("%s: %w", e.Name, err)
			}
			if e.Delay > 0 {
				logx.Debugf("maskrom: wait %d ms after %s", e.Delay, e.Name)
				time.Sleep(time.Duration(e.Delay) * time.Millisecond)
			}
		}
	}
	return nil
}
