// Package usbfs accesses Rockchip USB devices. On Linux it uses usbfs
// directly (no libusb, no cgo). Other systems get a stub that reports
// USB access as unsupported; the offline image tools still work there.
package usbfs

import (
	"errors"
	"fmt"
)

// RockchipVID is the Rockchip USB vendor ID.
const RockchipVID = 0x2207

// ErrUnsupported reports that USB access is not available on this system.
var ErrUnsupported = errors.New("USB device access is supported on Linux only")

// Mode is the boot stage of a Rockchip device.
type Mode string

const (
	ModeMaskrom Mode = "Maskrom"
	ModeLoader  Mode = "Loader"
	ModeMSC     Mode = "MSC"
)

// DeviceInfo describes one Rockchip USB device found in sysfs.
type DeviceInfo struct {
	SysPath string
	Port    string // sysfs name, e.g. "3-1.2"
	Bus     int
	Dev     int
	PID     uint16
	BcdUSB  uint16
	Iface   int
	EpIn    uint8
	EpOut   uint8
	Serial  string
}

// Mode returns the boot stage. The loader reports an odd bcdUSB.
func (d DeviceInfo) Mode() Mode {
	if d.EpIn == 0 && d.EpOut == 0 {
		return ModeMSC
	}
	if d.BcdUSB&1 == 1 {
		return ModeLoader
	}
	return ModeMaskrom
}

// Node returns the usbfs device node path.
func (d DeviceInfo) Node() string {
	return fmt.Sprintf("/dev/bus/usb/%03d/%03d", d.Bus, d.Dev)
}
