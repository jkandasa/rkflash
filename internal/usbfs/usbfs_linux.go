//go:build linux

package usbfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/jkandasa/rkflash/internal/logx"
)

type ctrlTransfer struct {
	RequestType uint8
	Request     uint8
	Value       uint16
	Index       uint16
	Length      uint16
	Timeout     uint32 // ms
	Data        uintptr
}

type bulkTransfer struct {
	Ep      uint32
	Len     uint32
	Timeout uint32 // ms
	Data    uintptr
}

type usbIoctl struct {
	Ifno      int32
	IoctlCode int32
	Data      uintptr
}

func iowr(nr, size uintptr) uintptr { return 3<<30 | size<<16 | 'U'<<8 | nr }
func ior(nr, size uintptr) uintptr  { return 2<<30 | size<<16 | 'U'<<8 | nr }
func ioc(nr uintptr) uintptr        { return 'U'<<8 | nr }

var (
	ioctlControl    = iowr(0, unsafe.Sizeof(ctrlTransfer{}))
	ioctlBulk       = iowr(2, unsafe.Sizeof(bulkTransfer{}))
	ioctlClaim      = ior(15, 4)
	ioctlRelease    = ior(16, 4)
	ioctlIoctl      = iowr(18, unsafe.Sizeof(usbIoctl{}))
	ioctlReset      = ioc(20)
	ioctlClearHalt  = ior(21, 4)
	ioctlDisconnect = ioc(22)
)

func readSys(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readSysHex(path string) (uint64, error) {
	return strconv.ParseUint(readSys(path), 16, 32)
}

// List scans sysfs for Rockchip devices.
func List() ([]DeviceInfo, error) {
	entries, err := filepath.Glob("/sys/bus/usb/devices/*")
	if err != nil {
		return nil, err
	}
	var out []DeviceInfo
	for _, p := range entries {
		base := filepath.Base(p)
		if strings.Contains(base, ":") {
			continue
		}
		vid, err := readSysHex(filepath.Join(p, "idVendor"))
		if err != nil || vid != RockchipVID {
			continue
		}
		pid, _ := readSysHex(filepath.Join(p, "idProduct"))
		bus, _ := strconv.Atoi(readSys(filepath.Join(p, "busnum")))
		dev, _ := strconv.Atoi(readSys(filepath.Join(p, "devnum")))
		d := DeviceInfo{SysPath: p, Port: base, Bus: bus, Dev: dev, PID: uint16(pid),
			Serial: readSys(filepath.Join(p, "serial"))}
		// sysfs "version" holds bcdUSB as " 2.01".
		if parts := strings.SplitN(readSys(filepath.Join(p, "version")), ".", 2); len(parts) == 2 {
			maj, _ := strconv.ParseUint(parts[0], 16, 8)
			mnr, _ := strconv.ParseUint(parts[1], 16, 8)
			d.BcdUSB = uint16(maj<<8 | mnr)
		}
		ifaces, _ := filepath.Glob(filepath.Join(p, base+":1.*"))
		for _, ifp := range ifaces {
			eps, _ := filepath.Glob(filepath.Join(ifp, "ep_*"))
			var in, outEp uint8
			for _, ep := range eps {
				if readSys(filepath.Join(ep, "type")) != "Bulk" {
					continue
				}
				addr, err := readSysHex(filepath.Join(ep, "bEndpointAddress"))
				if err != nil {
					continue
				}
				if addr&0x80 != 0 {
					in = uint8(addr)
				} else {
					outEp = uint8(addr)
				}
			}
			if in != 0 && outEp != 0 {
				n, _ := strconv.ParseUint(readSys(filepath.Join(ifp, "bInterfaceNumber")), 16, 8)
				d.Iface, d.EpIn, d.EpOut = int(n), in, outEp
				break
			}
		}
		logx.Debugf("usb: found %s bus %d dev %d pid %04x bcdUSB %04x iface %d ep-in %02x ep-out %02x",
			d.Port, d.Bus, d.Dev, d.PID, d.BcdUSB, d.Iface, d.EpIn, d.EpOut)
		out = append(out, d)
	}
	return out, nil
}

// Device is an open usbfs handle with the bulk interface claimed.
type Device struct {
	Info DeviceInfo
	f    *os.File
}

func ioctl(fd uintptr, req uintptr, arg uintptr) (int, error) {
	r, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg)
	if e != 0 {
		return int(r), e
	}
	return int(r), nil
}

// Open opens the device node and claims the bulk interface.
func Open(info DeviceInfo) (*Device, error) {
	node := info.Node()
	logx.Debugf("usb: open %s", node)
	f, err := os.OpenFile(node, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("open %s: %w (run as root or install the udev rule: rkflash udev-rule)", node, err)
		}
		return nil, fmt.Errorf("open %s: %w", node, err)
	}
	d := &Device{Info: info, f: f}
	iface := uint32(info.Iface)
	_, err = ioctl(f.Fd(), ioctlClaim, uintptr(unsafe.Pointer(&iface)))
	if errors.Is(err, syscall.EBUSY) {
		logx.Debugf("usb: interface %d busy, detaching kernel driver", iface)
		req := usbIoctl{Ifno: int32(iface), IoctlCode: int32(ioctlDisconnect)}
		ioctl(f.Fd(), ioctlIoctl, uintptr(unsafe.Pointer(&req)))
		_, err = ioctl(f.Fd(), ioctlClaim, uintptr(unsafe.Pointer(&iface)))
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("claim interface %d: %w (is another tool using the device?)", iface, err)
	}
	return d, nil
}

// Close releases the interface and closes the node.
func (d *Device) Close() error {
	iface := uint32(d.Info.Iface)
	ioctl(d.f.Fd(), ioctlRelease, uintptr(unsafe.Pointer(&iface)))
	return d.f.Close()
}

func ms(t time.Duration) uint32 {
	if t <= 0 {
		return 0
	}
	return uint32(t / time.Millisecond)
}

// ControlOut sends a vendor OUT control transfer.
func (d *Device) ControlOut(reqType, req uint8, value, index uint16, data []byte, timeout time.Duration) (int, error) {
	ct := ctrlTransfer{RequestType: reqType, Request: req, Value: value, Index: index,
		Length: uint16(len(data)), Timeout: ms(timeout)}
	if len(data) > 0 {
		ct.Data = uintptr(unsafe.Pointer(&data[0]))
	}
	n, err := ioctl(d.f.Fd(), ioctlControl, uintptr(unsafe.Pointer(&ct)))
	runtime.KeepAlive(data)
	logx.Tracef("usb: control out type %02x req %02x idx %04x len %d -> %d %v", reqType, req, index, len(data), n, err)
	return n, err
}

func (d *Device) bulk(ep uint8, data []byte, timeout time.Duration) (int, error) {
	bt := bulkTransfer{Ep: uint32(ep), Len: uint32(len(data)), Timeout: ms(timeout)}
	if len(data) > 0 {
		bt.Data = uintptr(unsafe.Pointer(&data[0]))
	}
	n, err := ioctl(d.f.Fd(), ioctlBulk, uintptr(unsafe.Pointer(&bt)))
	runtime.KeepAlive(data)
	return n, err
}

// BulkOut writes all of data to the OUT endpoint.
func (d *Device) BulkOut(data []byte, timeout time.Duration) error {
	n, err := d.bulk(d.Info.EpOut, data, timeout)
	logx.Tracef("usb: bulk out %d bytes -> %d %v", len(data), n, err)
	if err != nil {
		return fmt.Errorf("bulk out: %w", err)
	}
	if n != len(data) {
		return fmt.Errorf("bulk out: short write %d/%d", n, len(data))
	}
	return nil
}

// BulkIn reads up to len(data) bytes from the IN endpoint.
func (d *Device) BulkIn(data []byte, timeout time.Duration) (int, error) {
	n, err := d.bulk(d.Info.EpIn, data, timeout)
	logx.Tracef("usb: bulk in %d bytes -> %d %v", len(data), n, err)
	if err != nil {
		return n, fmt.Errorf("bulk in: %w", err)
	}
	return n, nil
}

// ClearHalt clears a stall on the IN or OUT endpoint.
func (d *Device) ClearHalt(in bool) {
	ep := uint32(d.Info.EpOut)
	if in {
		ep = uint32(d.Info.EpIn)
	}
	logx.Debugf("usb: clear halt ep %02x", ep)
	ioctl(d.f.Fd(), ioctlClearHalt, uintptr(unsafe.Pointer(&ep)))
}

// PortReset resets the USB device.
func (d *Device) PortReset() error {
	_, err := ioctl(d.f.Fd(), ioctlReset, 0)
	return err
}
