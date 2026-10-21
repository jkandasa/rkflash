// Package rockusb implements the Rockchip USB protocols.
//
// Loader mode uses a mass-storage style CBW/CSW exchange on bulk endpoints.
// Maskrom mode uses vendor control requests (see maskrom.go).
package rockusb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jkandasa/rkflash/internal/logx"
)

// SectorSize is the LBA unit of the protocol.
const SectorSize = 512

// MaxSectors is the largest sector count of one LBA command (16-bit field).
const MaxSectors = 0xffff

const (
	cbwSign = 0x43425355 // "USBC"
	cswSign = 0x53425355 // "USBS"
	cbwLen  = 31
	cswLen  = 13
)

// Operation codes, as in rkdeveloptool.
const (
	OpTestUnitReady = 0x00
	OpReadFlashID   = 0x01
	OpTestBadBlock  = 0x03
	OpReadSector    = 0x04
	OpWriteSector   = 0x05
	OpEraseNormal   = 0x06
	OpEraseForce    = 0x0B
	OpReadLBA       = 0x14
	OpWriteLBA      = 0x15
	OpEraseSysDisk  = 0x16
	OpReadSDRAM     = 0x17
	OpWriteSDRAM    = 0x18
	OpExecSDRAM     = 0x19
	OpReadFlashInfo = 0x1A
	OpReadChipInfo  = 0x1B
	OpSetResetFlag  = 0x1E
	OpEraseLBA      = 0x25
	OpReadCapab     = 0xAA
	OpDeviceReset   = 0xFF
)

var opNames = map[uint8]string{
	OpTestUnitReady: "TEST_UNIT_READY", OpReadFlashID: "READ_FLASH_ID",
	OpReadLBA: "READ_LBA", OpWriteLBA: "WRITE_LBA", OpReadFlashInfo: "READ_FLASH_INFO",
	OpReadChipInfo: "READ_CHIP_INFO", OpEraseLBA: "ERASE_LBA", OpReadCapab: "READ_CAPABILITY",
	OpDeviceReset: "DEVICE_RESET",
}

// OpName returns a readable name of an operation code.
func OpName(op uint8) string {
	if n, ok := opNames[op]; ok {
		return n
	}
	return fmt.Sprintf("OP_%02X", op)
}

// Reset subcodes for DEVICE_RESET.
const (
	ResetNormal     = 0
	ResetMSC        = 1
	ResetPowerOff   = 2
	ResetMaskrom    = 3
	ResetDisconnect = 4
)

// Transport is the bulk pipe to a device in Loader mode.
type Transport interface {
	BulkOut(p []byte, timeout time.Duration) error
	BulkIn(p []byte, timeout time.Duration) (int, error)
	ClearHalt(in bool)
}

// ErrCommandFailed reports a CSW with a nonzero status.
var ErrCommandFailed = errors.New("device reported command failure")

// Client sends rockusb commands over a Transport.
type Client struct {
	t Transport
	// Timeout applies to data stages and ordinary commands.
	Timeout time.Duration
	// EraseTimeout applies to the CSW of erase commands.
	EraseTimeout time.Duration
}

// NewClient returns a Client with default timeouts.
func NewClient(t Transport) *Client {
	return &Client{t: t, Timeout: 20 * time.Second, EraseTimeout: 5 * time.Minute}
}

type spec struct {
	in    bool
	cbLen uint8
}

func specOf(op uint8) spec {
	switch op {
	case OpTestUnitReady, OpReadFlashID, OpReadFlashInfo, OpReadChipInfo, OpReadCapab:
		return spec{true, 6}
	case OpDeviceReset, OpEraseSysDisk, OpSetResetFlag:
		return spec{false, 6}
	case OpTestBadBlock, OpReadSector, OpReadLBA, OpReadSDRAM:
		return spec{true, 10}
	default:
		return spec{false, 10}
	}
}

// CBW is a command block wrapper.
type CBW struct {
	Tag     uint32
	XferLen uint32
	In      bool
	CBLen   uint8
	Op      uint8
	Sub     uint8
	Address uint32 // big-endian on the wire
	Length  uint16 // big-endian on the wire
}

// Marshal encodes the CBW.
func (c CBW) Marshal() []byte {
	b := make([]byte, cbwLen)
	binary.LittleEndian.PutUint32(b[0:], cbwSign)
	binary.LittleEndian.PutUint32(b[4:], c.Tag)
	binary.LittleEndian.PutUint32(b[8:], c.XferLen)
	if c.In {
		b[12] = 0x80
	}
	b[14] = c.CBLen
	cb := b[15:]
	cb[0] = c.Op
	cb[1] = c.Sub
	binary.BigEndian.PutUint32(cb[2:], c.Address)
	binary.BigEndian.PutUint16(cb[7:], c.Length)
	return b
}

// ParseCBW decodes a CBW (used by the simulator).
func ParseCBW(b []byte) (CBW, error) {
	if len(b) != cbwLen || binary.LittleEndian.Uint32(b) != cbwSign {
		return CBW{}, fmt.Errorf("bad CBW")
	}
	cb := b[15:]
	return CBW{
		Tag: binary.LittleEndian.Uint32(b[4:]), XferLen: binary.LittleEndian.Uint32(b[8:]),
		In: b[12]&0x80 != 0, CBLen: b[14], Op: cb[0], Sub: cb[1],
		Address: binary.BigEndian.Uint32(cb[2:]), Length: binary.BigEndian.Uint16(cb[7:]),
	}, nil
}

// MarshalCSW encodes a CSW (used by the simulator).
func MarshalCSW(tag, residue uint32, status uint8) []byte {
	b := make([]byte, cswLen)
	binary.LittleEndian.PutUint32(b[0:], cswSign)
	binary.LittleEndian.PutUint32(b[4:], tag)
	binary.LittleEndian.PutUint32(b[8:], residue)
	b[12] = status
	return b
}

func newCBW(op, sub uint8, addr uint32, length uint16, xfer uint32) CBW {
	s := specOf(op)
	return CBW{Tag: rand.Uint32(), XferLen: xfer, In: s.in, CBLen: s.cbLen,
		Op: op, Sub: sub, Address: addr, Length: length}
}

func (c *Client) readCSW(tag uint32, timeout time.Duration) error {
	csw := make([]byte, cswLen)
	n, err := c.t.BulkIn(csw, timeout)
	if err != nil {
		c.t.ClearHalt(true)
		return fmt.Errorf("read CSW: %w", err)
	}
	return checkCSW(csw[:n], tag)
}

// isCSW reports whether a data-stage packet is really a CSW.
// A device that rejects a command can skip the data stage.
func isCSW(b []byte, want int) bool {
	return len(b) == cswLen && want != cswLen && binary.LittleEndian.Uint32(b) == cswSign
}

func checkCSW(csw []byte, tag uint32) error {
	n := len(csw)
	if n != cswLen || binary.LittleEndian.Uint32(csw) != cswSign {
		return fmt.Errorf("bad CSW (%d bytes: % x)", n, csw[:n])
	}
	if got := binary.LittleEndian.Uint32(csw[4:]); got != tag {
		return fmt.Errorf("CSW tag mismatch (want %08x, got %08x)", tag, got)
	}
	logx.Tracef("rockusb: CSW tag %08x residue %d status %d", tag, binary.LittleEndian.Uint32(csw[8:]), csw[12])
	if csw[12] != 0 {
		return fmt.Errorf("%w (CSW status %d)", ErrCommandFailed, csw[12])
	}
	return nil
}

// Exec runs one command. IN commands fill buf. OUT commands send buf.
// It returns the number of data bytes moved.
func (c *Client) Exec(op, sub uint8, addr uint32, length uint16, xfer uint32, buf []byte, cswTimeout time.Duration) (int, error) {
	cbw := newCBW(op, sub, addr, length, xfer)
	start := time.Now()
	logx.Debugf("rockusb: %s sub %d addr 0x%x len %d xfer %d", OpName(op), sub, addr, length, xfer)
	if err := c.t.BulkOut(cbw.Marshal(), c.Timeout); err != nil {
		return 0, fmt.Errorf("%s: send CBW: %w", OpName(op), err)
	}
	n := 0
	if xfer > 0 {
		if cbw.In {
			var err error
			n, err = c.t.BulkIn(buf[:xfer], c.Timeout)
			if err != nil {
				c.t.ClearHalt(true)
				return n, fmt.Errorf("%s: data in: %w", OpName(op), err)
			}
			if isCSW(buf[:n], int(xfer)) {
				logx.Debugf("rockusb: %s: device skipped the data stage", OpName(op))
				if err := checkCSW(buf[:n], cbw.Tag); err != nil {
					return 0, fmt.Errorf("%s: %w", OpName(op), err)
				}
				return 0, fmt.Errorf("%s: device sent no data", OpName(op))
			}
			logx.Dump("rockusb: in ", buf[:n], 64)
		} else {
			logx.Dump("rockusb: out", buf[:xfer], 64)
			if err := c.t.BulkOut(buf[:xfer], c.Timeout); err != nil {
				c.t.ClearHalt(false)
				return 0, fmt.Errorf("%s: data out: %w", OpName(op), err)
			}
			n = int(xfer)
		}
	}
	if err := c.readCSW(cbw.Tag, cswTimeout); err != nil {
		return n, fmt.Errorf("%s: %w", OpName(op), err)
	}
	logx.Debugf("rockusb: %s done, %d bytes in %s", OpName(op), n, time.Since(start).Round(time.Microsecond))
	return n, nil
}

// TestUnitReady checks that the loader is ready.
func (c *Client) TestUnitReady() error {
	_, err := c.Exec(OpTestUnitReady, 0, 0, 0, 0, nil, c.Timeout)
	return err
}

func (c *Client) readSmall(op uint8, size uint32) ([]byte, error) {
	buf := make([]byte, size)
	n, err := c.Exec(op, 0, 0, 0, size, buf, c.Timeout)
	return buf[:n], err
}

// FlashID returns the 5-byte flash ID, e.g. "EMMC ".
func (c *Client) FlashID() ([]byte, error) { return c.readSmall(OpReadFlashID, 5) }

// ChipInfo returns the 16-byte chip info.
func (c *Client) ChipInfo() ([]byte, error) { return c.readSmall(OpReadChipInfo, 16) }

// Capability returns the 8-byte capability field. Old loaders do not answer.
func (c *Client) Capability() ([]byte, error) { return c.readSmall(OpReadCapab, 8) }

// FlashInfo holds the loader's storage report.
type FlashInfo struct {
	Sectors   uint32 // storage size in 512-byte sectors
	BlockSize uint16 // in sectors
	PageSize  uint8  // in sectors
	ECCBits   uint8
	Access    uint8
	Manuf     uint8
	CS        uint8
	Raw       []byte
}

// Bytes returns the storage size in bytes.
func (f *FlashInfo) Bytes() uint64 { return uint64(f.Sectors) * SectorSize }

// ParseFlashInfo decodes the READ_FLASH_INFO answer.
func ParseFlashInfo(b []byte) (*FlashInfo, error) {
	if len(b) < 11 {
		return nil, fmt.Errorf("short flash info (%d bytes)", len(b))
	}
	return &FlashInfo{
		Sectors:   binary.LittleEndian.Uint32(b[0:]),
		BlockSize: binary.LittleEndian.Uint16(b[4:]),
		PageSize:  b[6], ECCBits: b[7], Access: b[8], Manuf: b[9], CS: b[10],
		Raw: append([]byte(nil), b...),
	}, nil
}

// FlashInfo reads the storage report.
func (c *Client) FlashInfo() (*FlashInfo, error) {
	// rkdeveloptool reads up to 512 bytes. Loaders answer with 11.
	buf := make([]byte, 512)
	cbw := newCBW(OpReadFlashInfo, 0, 0, 0, 11)
	logx.Debugf("rockusb: %s", OpName(OpReadFlashInfo))
	if err := c.t.BulkOut(cbw.Marshal(), c.Timeout); err != nil {
		return nil, err
	}
	n, err := c.t.BulkIn(buf, c.Timeout)
	if err != nil {
		return nil, err
	}
	if isCSW(buf[:n], 11) {
		if err := checkCSW(buf[:n], cbw.Tag); err != nil {
			return nil, fmt.Errorf("%s: %w", OpName(OpReadFlashInfo), err)
		}
		return nil, fmt.Errorf("%s: device sent no data", OpName(OpReadFlashInfo))
	}
	if err := c.readCSW(cbw.Tag, c.Timeout); err != nil {
		return nil, err
	}
	logx.Dump("rockusb: flash info", buf[:n], 32)
	return ParseFlashInfo(buf[:n])
}

// ReadLBA reads count sectors at lba into buf.
func (c *Client) ReadLBA(lba uint32, count uint16, buf []byte) error {
	size := uint32(count) * SectorSize
	n, err := c.Exec(OpReadLBA, 0, lba, count, size, buf, c.Timeout)
	if err != nil {
		return err
	}
	if uint32(n) != size {
		return fmt.Errorf("READ_LBA: short read %d/%d", n, size)
	}
	return nil
}

// WriteLBA writes count sectors from buf at lba.
func (c *Client) WriteLBA(lba uint32, count uint16, buf []byte) error {
	_, err := c.Exec(OpWriteLBA, 0, lba, count, uint32(count)*SectorSize, buf, c.Timeout)
	return err
}

// EraseLBA erases count sectors at lba. eMMC erase can be slow.
func (c *Client) EraseLBA(lba uint32, count uint16) error {
	_, err := c.Exec(OpEraseLBA, 0, lba, count, 0, nil, c.EraseTimeout)
	return err
}

// Reset reboots the device. The device can leave the bus before the CSW.
func (c *Client) Reset(sub uint8) error {
	cbw := newCBW(OpDeviceReset, sub, 0, 0, 0)
	logx.Debugf("rockusb: %s sub %d", OpName(OpDeviceReset), sub)
	if err := c.t.BulkOut(cbw.Marshal(), c.Timeout); err != nil {
		return err
	}
	csw := make([]byte, cswLen)
	if _, err := c.t.BulkIn(csw, 2*time.Second); err != nil {
		logx.Debugf("rockusb: no CSW after reset (expected): %v", err)
	}
	return nil
}

// Capability bits in byte 0 (and byte 1 bit 0 for NewIDB).
var CapabilityBits = []struct {
	Byte, Mask uint8
	Name       string
}{
	{0, 0x01, "Direct LBA"},
	{0, 0x02, "Vendor Storage"},
	{0, 0x04, "First 4M Access"},
	{0, 0x08, "Read LBA"},
	{0, 0x10, "Read Com Log"},
	{0, 0x20, "Read IDB Config"},
	{0, 0x40, "Read Secure Mode"},
	{1, 0x01, "New IDB"},
}
