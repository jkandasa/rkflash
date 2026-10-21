package rockusb

// knownPIDs maps Rockchip USB product IDs to SoC names.
var knownPIDs = map[uint16]string{
	0x301a: "RK3036",
	0x310b: "RK3188",
	0x310c: "RK3126/RK3128",
	0x320a: "RK3288",
	0x320b: "RK3228/RK3229",
	0x320c: "RK3328",
	0x330a: "RK3368",
	0x330c: "RK3399",
	0x330d: "PX30",
	0x350a: "RK3568",
	0x350b: "RK3588",
}

// SoCName returns the SoC name for a product ID, or "".
func SoCName(pid uint16) string { return knownPIDs[pid] }

// ChipTag returns the 4-character chip tag from READ_CHIP_INFO.
// The device sends it byte-reversed, e.g. "A213" for "312A".
func ChipTag(info []byte) string {
	if len(info) < 4 {
		return ""
	}
	return string([]byte{info[3], info[2], info[1], info[0]})
}
