package rkimage

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func read(t *testing.T, path string) []byte {
	t.Helper()
	d, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// Reference images were made with U-Boot "mkimage -T rksd" (see testdata).
func TestBuildIDBMatchesMkimage(t *testing.T) {
	initCode := read(t, "../../testdata/init.bin")
	boot := read(t, "../../testdata/boot.bin")
	cases := []struct {
		chip, ref string
		boot      []byte
		append    bool
	}{
		{"rk3128", "idb-rk3128-append.img", boot, true},
		{"rk3128", "idb-rk3128-split.img", boot, false},
		{"rk3188", "idb-rk3188-split.img", boot, false}, // RC4 chip
		{"rk3288", "idb-rk3288-initonly.img", nil, false},
	}
	for _, tc := range cases {
		chip, err := FindChip(tc.chip)
		if err != nil {
			t.Fatal(err)
		}
		got, err := BuildIDB(chip, initCode, tc.boot, IDBOptions{Append: tc.append})
		if err != nil {
			t.Fatal(err)
		}
		if want := read(t, "../../testdata/"+tc.ref); !bytes.Equal(got, want) {
			t.Errorf("%s: output differs from mkimage (%d vs %d bytes)", tc.ref, len(got), len(want))
		}
		info, err := ParseIDB(got)
		if err != nil {
			t.Fatalf("%s: %v", tc.ref, err)
		}
		if info.SPLHdr != chip.SPLHdr || info.InitOffset != 4 || info.DisableRC4 == chip.RC4 {
			t.Errorf("%s: parsed %+v", tc.ref, info)
		}
	}
}

func TestBuildIDBTooLarge(t *testing.T) {
	chip, _ := FindChip("rk3128")
	if _, err := BuildIDB(chip, make([]byte, 0x2000), nil, IDBOptions{}); err == nil {
		t.Fatal("want size error")
	}
}

func TestCRCs(t *testing.T) {
	// Check values computed independently. The CRC16 equals CRC-16/CCITT-FALSE.
	if got := RKCRC32([]byte("123456789")); got != 0x889a9615 {
		t.Errorf("RKCRC32 = %08x", got)
	}
	if got := CRC16CCITT([]byte("123456789")); got != 0x29b1 {
		t.Errorf("CRC16 = %04x", got)
	}
}

func TestRC4Symmetric(t *testing.T) {
	d := []byte("hello rockchip, this is longer than nothing")
	orig := append([]byte(nil), d...)
	RC4(d)
	if bytes.Equal(d, orig) {
		t.Fatal("RC4 did nothing")
	}
	RC4(d)
	if !bytes.Equal(d, orig) {
		t.Fatal("RC4 is not symmetric")
	}
}

func TestParameter(t *testing.T) {
	p, err := ParseParameter(string(read(t, "../../testdata/parameter.txt")))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Parts) != 5 || p.Parts[0].Name != "uboot" || p.Parts[0].Offset != 0x2000 || p.Parts[0].Size != 0x2000 {
		t.Fatalf("parts %+v", p.Parts)
	}
	last := p.Parts[4]
	if last.Name != "rootfs" || !last.Grow || last.Offset != 0x16000 {
		t.Fatalf("grow part %+v", last)
	}
	if p.UUIDs["rootfs"] == "" {
		t.Fatal("uuid line not parsed")
	}
	blk := EncodeParmBlock(p.Text)
	text, err := DecodeParmBlock(blk)
	if err != nil || text != p.Text {
		t.Fatalf("PARM round trip: %v", err)
	}
	blk[10] ^= 1
	if _, err := DecodeParmBlock(blk); err == nil {
		t.Fatal("corrupt PARM accepted")
	}
}

// RKFLASH_TEST_DISK may name a disk image with a PARM block at sector 0.
func TestDiskParm(t *testing.T) {
	path := os.Getenv("RKFLASH_TEST_DISK")
	if path == "" {
		t.Skip("RKFLASH_TEST_DISK not set")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	head := make([]byte, 4096)
	f.Read(head)
	text, err := DecodeParmBlock(head)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseParameter(text); err != nil {
		t.Fatal(err)
	}
}

func TestLoaderImageRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte("u-boot"), 5000)
	for _, h := range []int{32, 20, 0} {
		img, err := PackLoaderImage(payload, LoaderImageOptions{LoadAddr: 0x60000000, HashLen: h})
		if err != nil {
			t.Fatal(err)
		}
		li, err := ParseLoaderImage(img)
		if err != nil {
			t.Fatal(err)
		}
		if !li.CRCOK || (h > 0 && !li.HashOK) || li.Copies != 4 || li.LoadAddr != 0x60000000 {
			t.Errorf("hash %d: %+v", h, li)
		}
		got, _, err := UnpackLoaderImage(img)
		if err != nil || !bytes.Equal(got, payload) {
			t.Errorf("hash %d: unpack mismatch", h)
		}
	}
}

// The reference was made by rkbin "loaderimage --pack --uboot boot.bin out 0x60000000".
func TestLoaderImageMatchesLoaderimage(t *testing.T) {
	f, err := os.Open("../../testdata/uboot-loaderimage.img.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	want, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	got, err := PackLoaderImage(read(t, "../../testdata/boot.bin"), LoaderImageOptions{LoadAddr: 0x60000000, HashLen: 32})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("output differs from loaderimage")
	}
}

// RKFLASH_TEST_IMAGES may name a directory of real uboot.img/trust.img files.
func TestRealLoaderImages(t *testing.T) {
	dir := os.Getenv("RKFLASH_TEST_IMAGES")
	if dir == "" {
		t.Skip("RKFLASH_TEST_IMAGES not set")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.img"))
	for _, path := range paths {
		d := read(t, path)
		li, err := ParseLoaderImage(d)
		if err != nil {
			continue
		}
		if !li.CRCOK || !li.HashOK {
			t.Errorf("%s: CRC %v hash %v", path, li.CRCOK, li.HashOK)
		}
	}
}

// rkbinDir returns RKFLASH_TEST_RKBIN (a checkout of rockchip-linux/rkbin) or skips.
func rkbinDir(t *testing.T) string {
	dir := os.Getenv("RKFLASH_TEST_RKBIN")
	if dir == "" {
		t.Skip("RKFLASH_TEST_RKBIN not set")
	}
	return dir
}

// The rkbin RK3128 loader: parse, check entries, and rebuild byte-identical.
func TestBootFileRoundTrip(t *testing.T) {
	rk := rkbinDir(t)
	d, err := os.ReadFile(filepath.Join(rk, "bin/rk31/rk3128_loader_v2.05.240.bin"))
	if err != nil {
		d, err = os.ReadFile(filepath.Join(rk, "rk31/rk3128_loader_v2.05.240.bin"))
	}
	if err != nil {
		t.Skip("rk3128 loader not found in RKFLASH_TEST_RKBIN")
	}
	bf, err := ParseBootFile(d)
	if err != nil {
		t.Fatal(err)
	}
	if bf.Chip() != "312A" || len(bf.Entries471) != 1 || len(bf.Entries472) != 1 || len(bf.EntriesLoader) != 2 {
		t.Fatalf("unexpected loader: chip %s", bf.Chip())
	}
	if !bytes.HasPrefix(bf.Entries471[0].Data, []byte("RK31")) || !bytes.HasPrefix(bf.FindLoader("FlashData").Data, []byte("RK31")) {
		t.Fatal("entries not decrypted")
	}
	out, err := bf.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, d) {
		t.Fatalf("rebuilt loader differs (%d vs %d bytes)", len(out), len(d))
	}
	// The ID block from FlashData+FlashBoot must match one from the plain blobs.
	ddr, err1 := os.ReadFile(filepath.Join(rk, "bin/rk31/rk3128_ddr_300MHz_v2.06.bin"))
	ml, err2 := os.ReadFile(filepath.Join(rk, "bin/rk31/rk312x_miniloader_v2.40.bin"))
	if err1 != nil || err2 != nil {
		ddr, err1 = os.ReadFile(filepath.Join(rk, "rk31/rk3128_ddr_300MHz_v2.06.bin"))
		ml, err2 = os.ReadFile(filepath.Join(rk, "rk31/rk312x_miniloader_v2.40.bin"))
	}
	if err1 != nil || err2 != nil {
		return
	}
	chip, _ := FindChip("rk3128")
	want, _ := BuildIDB(chip, ddr, ml, IDBOptions{Append: true})
	got, err := IDBFromBootFile(chip, bf)
	if err != nil {
		t.Fatal(err)
	}
	// The loader pads entries to 2 KiB; compare up to the miniloader size.
	if !bytes.Equal(got[:len(want)], want) {
		t.Fatal("ID block from loader differs from ID block from rkbin blobs")
	}
}
