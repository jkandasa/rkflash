package part

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkandasa/rkflash/internal/rkimage"
)

func TestGUIDRoundTrip(t *testing.T) {
	const s = "0FC63DAF-8483-4772-8E79-3D69D8477DE4"
	b, err := ParseGUID(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := FormatGUID(b); got != s {
		t.Fatalf("got %s", got)
	}
	if g := FormatGUID(RandomGUID()); g[14] != '4' {
		t.Fatalf("not a v4 GUID: %s", g)
	}
}

func buildImage(t *testing.T, sectors uint64) (string, *GPTImage) {
	t.Helper()
	text, err := os.ReadFile("../../testdata/parameter.txt")
	if err != nil {
		t.Fatal(err)
	}
	p, err := rkimage.ParseParameter(string(text))
	if err != nil {
		t.Fatal(err)
	}
	img, err := BuildGPT(sectors, GPTFromParameter(p))
	if err != nil {
		t.Fatal(err)
	}
	disk := make([]byte, sectors*SectorSize)
	copy(disk, img.Primary)
	copy(disk[img.BackupLBA*SectorSize:], img.Backup)
	path := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(path, disk, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, img
}

func TestBuildGPTDetect(t *testing.T) {
	const sectors = 0x40000 // 128 MiB
	path, img := buildImage(t, sectors)
	disk, _ := os.ReadFile(path)
	tbl, err := Detect(disk[:34*SectorSize])
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Kind != "gpt" || len(tbl.Partitions) != 5 {
		t.Fatalf("table %+v", tbl)
	}
	root, err := tbl.Find("ROOTFS")
	if err != nil {
		t.Fatal(err)
	}
	if root.Start != 0x16000 || root.End() != sectors-34 || root.UUID != "614E0000-0000-4B53-8000-1D28000054A9" {
		t.Fatalf("rootfs %+v", root)
	}
	if img.BackupLBA != sectors-33 {
		t.Fatalf("backup at %d", img.BackupLBA)
	}
}

// sfdisk validates CRCs and the backup table independently.
func TestBuildGPTWithSfdisk(t *testing.T) {
	if _, err := exec.LookPath("sfdisk"); err != nil {
		t.Skip("sfdisk not installed")
	}
	path, _ := buildImage(t, 0x40000)
	out, err := exec.Command("sfdisk", "--json", path).CombinedOutput()
	if err != nil {
		t.Fatalf("sfdisk: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "corrupt") {
		t.Fatalf("sfdisk reports corruption:\n%s", out)
	}
	var res struct {
		PartitionTable struct {
			Label      string `json:"label"`
			Partitions []struct {
				Start uint64 `json:"start"`
				Size  uint64 `json:"size"`
				Name  string `json:"name"`
			} `json:"partitions"`
		} `json:"partitiontable"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}
	pt := res.PartitionTable
	if pt.Label != "gpt" || len(pt.Partitions) != 5 || pt.Partitions[0].Name != "uboot" || pt.Partitions[0].Start != 0x2000 {
		t.Fatalf("sfdisk sees %+v", pt)
	}
	if v, err := exec.Command("sfdisk", "--verify", path).CombinedOutput(); err != nil {
		t.Fatalf("sfdisk --verify: %v\n%s", err, v)
	}
}

func TestBuildGPTErrors(t *testing.T) {
	if _, err := BuildGPT(0x10000, []GPTEntry{{Name: "a", Start: 10, Size: 10}}); err == nil {
		t.Error("want error for start before first usable sector")
	}
	if _, err := BuildGPT(0x10000, []GPTEntry{{Name: "a", Start: 64, Size: 100}, {Name: "b", Start: 100, Size: 10}}); err == nil {
		t.Error("want overlap error")
	}
	if _, err := BuildGPT(0x10000, []GPTEntry{{Name: "a", Start: 64, Size: 0x20000}}); err == nil {
		t.Error("want size error")
	}
}

func TestDetectMBRAndParm(t *testing.T) {
	head := make([]byte, 34*SectorSize)
	head[446+4] = 0x83
	head[446+8] = 0x00
	head[446+9] = 0x80 // start 0x8000
	head[446+12] = 0x00
	head[446+13] = 0x00
	head[446+14] = 0x10 // size 0x100000
	head[510], head[511] = 0x55, 0xaa
	tbl, err := Detect(head)
	if err != nil || tbl.Kind != "mbr" || len(tbl.Partitions) != 1 || tbl.Partitions[0].Start != 0x8000 || tbl.Partitions[0].Size != 0x100000 {
		t.Fatalf("mbr: %+v %v", tbl, err)
	}
	text, _ := os.ReadFile("../../testdata/parameter.txt")
	head = make([]byte, 34*SectorSize)
	copy(head, rkimage.EncodeParmBlock(string(text)))
	tbl, err = Detect(head)
	if err != nil || tbl.Kind != "parameter" || len(tbl.Partitions) != 5 {
		t.Fatalf("parm: %+v %v", tbl, err)
	}
}
