// Package sim emulates a Rockchip device in Loader mode for tests.
// It implements rockusb.Transport over an in-memory disk.
package sim

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/jkandasa/rkflash/internal/rockusb"
)

// Device is a simulated loader with an in-memory disk.
type Device struct {
	Disk      []byte
	FlashID   []byte
	ChipInfo  []byte
	NoCapab   bool // stall READ_CAPABILITY like old loaders
	Requests  map[uint16][][]byte
	ResetSubs []uint8

	cbw     *rockusb.CBW
	pending []byte // data for the next bulk-in
	csw     []byte
	writeTo int64 // disk offset of an expected OUT data stage
	status  uint8
}

// New returns a simulated device with a disk of the given sector count.
func New(sectors int) *Device {
	return &Device{
		Disk:     make([]byte, sectors*rockusb.SectorSize),
		FlashID:  []byte("EMMC "),
		ChipInfo: []byte("A2130000"),
		Requests: map[uint16][][]byte{},
		writeTo:  -1,
	}
}

func (d *Device) sectors() uint32 { return uint32(len(d.Disk) / rockusb.SectorSize) }

func (d *Device) inRange(lba uint32, n uint16) bool {
	return uint64(lba)+uint64(n) <= uint64(d.sectors())
}

// BulkOut receives a CBW or an OUT data stage.
func (d *Device) BulkOut(p []byte, _ time.Duration) error {
	if d.writeTo >= 0 {
		copy(d.Disk[d.writeTo:], p)
		d.writeTo = -1
		d.finish()
		return nil
	}
	cbw, err := rockusb.ParseCBW(p)
	if err != nil {
		return err
	}
	d.cbw, d.status, d.pending, d.csw = &cbw, 0, nil, nil
	ss := uint64(rockusb.SectorSize)
	switch cbw.Op {
	case rockusb.OpTestUnitReady:
	case rockusb.OpReadFlashID:
		d.pending = d.FlashID
	case rockusb.OpReadChipInfo:
		d.pending = d.ChipInfo
	case rockusb.OpReadCapab:
		if d.NoCapab {
			return nil // never answer
		}
		d.pending = []byte{0x0f, 0, 0, 0, 0, 0, 0, 0}
	case rockusb.OpReadFlashInfo:
		b := make([]byte, 11)
		binary.LittleEndian.PutUint32(b, d.sectors())
		binary.LittleEndian.PutUint16(b[4:], 1024)
		b[6] = 4
		d.pending = b
	case rockusb.OpReadLBA:
		if !d.inRange(cbw.Address, cbw.Length) {
			d.status = 1
			break
		}
		off := uint64(cbw.Address) * ss
		d.pending = append([]byte(nil), d.Disk[off:off+uint64(cbw.Length)*ss]...)
	case rockusb.OpWriteLBA:
		if !d.inRange(cbw.Address, cbw.Length) {
			return fmt.Errorf("sim: write outside disk")
		}
		d.writeTo = int64(uint64(cbw.Address) * ss)
		return nil // CSW after the data stage
	case rockusb.OpEraseLBA:
		if !d.inRange(cbw.Address, cbw.Length) {
			d.status = 1
			break
		}
		off := uint64(cbw.Address) * ss
		clear(d.Disk[off : off+uint64(cbw.Length)*ss])
	case rockusb.OpDeviceReset:
		d.ResetSubs = append(d.ResetSubs, cbw.Sub)
	default:
		d.status = 1
	}
	d.finish()
	return nil
}

func (d *Device) finish() {
	d.csw = rockusb.MarshalCSW(d.cbw.Tag, 0, d.status)
}

// BulkIn returns pending data, then the CSW.
func (d *Device) BulkIn(p []byte, _ time.Duration) (int, error) {
	if d.pending != nil {
		n := copy(p, d.pending)
		d.pending = nil
		return n, nil
	}
	if d.csw == nil {
		return 0, fmt.Errorf("sim: timeout")
	}
	n := copy(p, d.csw)
	d.csw = nil
	return n, nil
}

// ClearHalt is a no-op.
func (d *Device) ClearHalt(bool) {}

// ControlOut records Maskrom requests.
func (d *Device) ControlOut(_, _ uint8, _, index uint16, data []byte, _ time.Duration) (int, error) {
	d.Requests[index] = append(d.Requests[index], append([]byte(nil), data...))
	return len(data), nil
}
