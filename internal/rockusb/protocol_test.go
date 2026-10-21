package rockusb_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/jkandasa/rkflash/internal/rkimage"
	"github.com/jkandasa/rkflash/internal/rockusb"
	"github.com/jkandasa/rkflash/internal/sim"
)

func TestCBWRoundTrip(t *testing.T) {
	c := rockusb.CBW{Tag: 0x1234, XferLen: 4096, In: true, CBLen: 10, Op: rockusb.OpReadLBA, Address: 0x4000, Length: 8}
	b := c.Marshal()
	if len(b) != 31 || string(b[:4]) != "USBC" {
		t.Fatalf("bad CBW % x", b)
	}
	// LBA and length are big-endian in the command block.
	if !bytes.Equal(b[17:21], []byte{0, 0, 0x40, 0}) || !bytes.Equal(b[22:24], []byte{0, 8}) {
		t.Fatalf("bad field encoding % x", b[15:31])
	}
	got, err := rockusb.ParseCBW(b)
	if err != nil || got != c {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestClientLBA(t *testing.T) {
	dev := sim.New(4096)
	cl := rockusb.NewClient(dev)
	data := bytes.Repeat([]byte{0xa5}, 8*512)
	if err := cl.WriteLBA(100, 8, data); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8*512)
	if err := cl.ReadLBA(100, 8, buf); err != nil || !bytes.Equal(buf, data) {
		t.Fatalf("read back: %v", err)
	}
	if err := cl.EraseLBA(100, 4); err != nil {
		t.Fatal(err)
	}
	cl.ReadLBA(100, 8, buf)
	if !bytes.Equal(buf[:4*512], make([]byte, 4*512)) || !bytes.Equal(buf[4*512:], data[4*512:]) {
		t.Fatal("erase range wrong")
	}
	if err := cl.ReadLBA(4090, 8, buf); !errors.Is(err, rockusb.ErrCommandFailed) {
		t.Fatalf("out of range read: %v", err)
	}
	fi, err := cl.FlashInfo()
	if err != nil || fi.Sectors != 4096 {
		t.Fatalf("flash info %+v %v", fi, err)
	}
	if id, _ := cl.FlashID(); string(id) != "EMMC " {
		t.Fatalf("flash id %q", id)
	}
	if ci, _ := cl.ChipInfo(); rockusb.ChipTag(ci) != "312A" {
		t.Fatalf("chip tag %q", rockusb.ChipTag(ci))
	}
}

func TestMaskromFraming(t *testing.T) {
	for _, n := range []int{100, 4094, 4095, 4096, 8190} {
		payload := bytes.Repeat([]byte{7}, n)
		dev := sim.New(1)
		if err := rockusb.DeviceRequest(dev, rockusb.Req471, payload); err != nil {
			t.Fatal(err)
		}
		var got []byte
		for _, ch := range dev.Requests[rockusb.Req471] {
			if len(ch) > 4096 {
				t.Fatalf("chunk of %d bytes", len(ch))
			}
			got = append(got, ch...)
		}
		data, pad := rockusb.MaskromPayload(payload)
		want := data
		if pad {
			want = append(append([]byte(nil), data...), 0)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("n=%d: sent %d bytes, want %d", n, len(got), len(want))
		}
		body := data[:len(data)-2]
		crc := rkimage.CRC16CCITT(body)
		if data[len(data)-2] != byte(crc>>8) || data[len(data)-1] != byte(crc) {
			t.Fatalf("n=%d: bad CRC trailer", n)
		}
		if n%4096 == 4095 && len(body) != n+1 {
			t.Fatalf("n=%d: missing pad byte", n)
		}
	}
}
