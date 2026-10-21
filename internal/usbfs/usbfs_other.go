//go:build !linux

package usbfs

import "time"

// List reports that USB access is unsupported.
func List() ([]DeviceInfo, error) { return nil, ErrUnsupported }

// Device is a placeholder on systems without usbfs.
type Device struct{ Info DeviceInfo }

// Open reports that USB access is unsupported.
func Open(DeviceInfo) (*Device, error) { return nil, ErrUnsupported }

// Close does nothing.
func (d *Device) Close() error { return nil }

// ControlOut reports that USB access is unsupported.
func (d *Device) ControlOut(_, _ uint8, _, _ uint16, _ []byte, _ time.Duration) (int, error) {
	return 0, ErrUnsupported
}

// BulkOut reports that USB access is unsupported.
func (d *Device) BulkOut([]byte, time.Duration) error { return ErrUnsupported }

// BulkIn reports that USB access is unsupported.
func (d *Device) BulkIn([]byte, time.Duration) (int, error) { return 0, ErrUnsupported }

// ClearHalt does nothing.
func (d *Device) ClearHalt(bool) {}

// PortReset reports that USB access is unsupported.
func (d *Device) PortReset() error { return ErrUnsupported }
