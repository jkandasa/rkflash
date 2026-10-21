# rkflash

Flash, back up, and inspect Rockchip devices over USB, and build or check
Rockchip boot images.

- Works in **Maskrom** and **Loader** mode (Rockchip "rockusb" protocol).
- Covers the `rkdeveloptool` command set, plus offline image tools.
- Pure Go, Linux usbfs. No libusb, no cgo, one static binary.
- Verbose diagnostics at three levels (`-v`, `-vv`, `-vvv`).

## Download

Prebuilt binaries are on the GitHub Releases page:

- `v*` releases: tagged versions.
- `devel`: pre-release, rebuilt from `main` on every push.

| System | Files |
|---|---|
| Linux | `rkflash-linux-amd64`, `-arm64`, `-armv7` |
| macOS | `rkflash-darwin-amd64`, `-arm64` |
| Windows | `rkflash-windows-amd64.exe` |

`SHA256SUMS` lists the checksums. USB commands need Linux. On macOS and
Windows, the offline image commands work (`image-info`, `idb`, `pack`,
`pack-uboot`, `gpt -o`, ...).

## Build and install

Requires Go 1.27 or newer.

```bash
go install github.com/jkandasa/rkflash/cmd/rkflash@latest
```

Or from a checkout:

```bash
make build          # ./rkflash
make test           # unit and end-to-end tests (simulated device)
sudo make install   # /usr/local/bin/rkflash
```

USB access needs root, or a udev rule:

```bash
rkflash udev-rule | sudo tee /etc/udev/rules.d/99-rockchip-usb.rules
sudo udevadm control --reload-rules && sudo udevadm trigger
```

## Device modes

| Mode    | How to get there                                 | What works           |
| ------- | ------------------------------------------------ | -------------------- |
| Maskrom | Empty boot storage, or the recovery button/short | `db` only            |
| Loader  | After `db`, or a loader that waits for USB       | all storage commands |

`rkflash list` shows the mode. In Maskrom mode, run `db` first.

## Quick start

```bash
rkflash list
rkflash db rk3128_loader_v2.05.240.bin      # Maskrom -> Loader
rkflash info                                # flash ID, chip, storage size
rkflash read 0 all backup.img               # full backup
rkflash write -verify 0 Armbian.img         # flash a disk image
rkflash reset
```

## Commands

Run `rkflash help <command>` for all flags. The `rkdeveloptool` names work
as aliases.

### Device (USB)

| Command                        | Alias | Description                                                       |
| ------------------------------ | ----- | ----------------------------------------------------------------- |
| `list`                         | `ld`  | List devices: bus:dev, port, VID:PID, mode, SoC                   |
| `wait`                         |       | Wait for a device in a given mode (`-mode loader`)                |
| `download-boot <loader>`       | `db`  | Maskrom: load DDR init and USB plug into RAM                      |
| `upgrade-loader <loader\|idb>` | `ul`  | Write the ID block to sector 64                                   |
| `info`                         |       | Flash ID, chip info, storage info (`-capability` for more)        |
| `flash-id`                     | `rid` | Flash ID, e.g. `EMMC`                                             |
| `chip-info`                    | `rci` | Chip info, e.g. `312A`                                            |
| `flash-info`                   | `rfi` | Storage size and geometry                                         |
| `capability`                   | `rcb` | Loader capability bits (newer loaders only)                       |
| `test`                         | `td`  | Test unit ready                                                   |
| `reset [mode]`                 | `rd`  | `normal`, `msc`, `poweroff`, `maskrom`, `disconnect`, or a number |

### Storage (Loader mode)

| Command                              | Alias | Description                                  |
| ------------------------------------ | ----- | -------------------------------------------- |
| `read <start> <count\|all> <file>`   | `rl`  | Read sectors to a file                       |
| `write [-verify] <start> <file>`     | `wl`  | Write a file                                 |
| `erase <start> <count\|all>`         | `el`  | ERASE_LBA over a range                       |
| `erase-flash`                        | `ef`  | Erase all storage (asks first; slow on eMMC) |
| `zero <start> <count>`               |       | Write zeros; fast wipe of a region           |
| `read-part <name> <file>`            | `rlx` | Read a partition by name                     |
| `write-part [-verify] <name> <file>` | `wlx` | Write a partition by name                    |

### Partitions

| Command                                | Alias | Description                              |
| -------------------------------------- | ----- | ---------------------------------------- |
| `print-partitions [-f image]`          | `ppt` | Show the GPT, MBR, or RK parameter table |
| `gpt [-o image] <parameter.txt>`       |       | Build and write a GPT from mtdparts      |
| `parameter [-o image] <parameter.txt>` | `prm` | Write a legacy PARM block (8 copies)     |

### Images (offline, no device)

| Command                                  | Description                                                   |
| ---------------------------------------- | ------------------------------------------------------------- |
| `image-info <file>` (`ii`)               | Identify and verify any supported file, including disk images |
| `unpack [-o dir] <loader>`               | Extract loader entries (decrypted; `-raw` keeps RC4)          |
| `pack -o <out> -chip 312A ...`           | Build a loader file, like `boot_merger`                       |
| `idb -o <out> ...`                       | Build an ID block, byte-identical to `mkimage -T rksd`        |
| `pack-uboot -addr <addr> -o <out> <bin>` | Build `uboot.img`/`trust.img`, like `loaderimage --pack`      |
| `unpack-uboot <img> <out>`               | Extract the payload of `uboot.img`/`trust.img`                |

### Numbers

Sector numbers and counts are in 512-byte sectors. `0x` gives hex. A `K`,
`M`, or `G` suffix gives a byte size: `16M` is 32768 sectors. `all` means
"to the end of storage".

## Global flags

| Flag                | Default | Description                                     |
| ------------------- | ------- | ----------------------------------------------- |
| `-d bus:dev`        |         | Select a device when more than one is connected |
| `-v`, `-vv`, `-vvv` | off     | Verbose logging (see below)                     |
| `-timeout`          | `20s`   | USB command timeout                             |
| `-erase-timeout`    | `5m`    | Timeout of one erase command                    |
| `-chunk`            | `128`   | Sectors per read/write command                  |
| `-y`                |         | Do not ask for confirmation                     |
| `-no-progress`      |         | Disable the progress bar                        |

Global flags work before or after the command.

## Verbose logging

Logs go to stderr with a timestamp:

| Level  | Shows                                                              |
| ------ | ------------------------------------------------------------------ |
| `-v`   | Device selection, operations, sizes, total time                    |
| `-vv`  | Each rockusb command: opcode, LBA, length, duration; USB discovery |
| `-vvv` | USB transfers, CSW status, hex dumps of command data               |

```bash
rkflash -vv info
rkflash -vvv rid 2> trace.log
```

`-vv` and above disable the progress bar so the log stays readable.

## Examples

Back up and restore one partition:

```bash
rkflash ppt
rkflash rlx boot boot.img
rkflash wlx -verify boot boot.img
```

Remove the boot loader from eMMC quickly, so the boot ROM falls back to SD:

```bash
rkflash zero 0 32M
```

Build boot images for an RK3128 board from rkbin blobs:

```bash
rkflash idb -chip rk3128 -init rk3128_ddr_300MHz_v2.06.bin \
    -boot rk312x_miniloader_v2.40.bin -o idbloader.img
rkflash pack-uboot -addr 0x60000000 -o uboot.img u-boot-dtb.bin
rkflash ii idbloader.img
rkflash ii uboot.img
```

Create a GPT in a disk image without a device:

```bash
rkflash gpt -o disk.img -disk-size 8G parameter.txt
rkflash ppt -f disk.img
```

## Exit status

`0` success, `1` error, `2` usage error, `130` interrupted (Ctrl+C).
Ctrl+C stops between chunks and reports the last sector.

## Notes and limits

- eMMC and SD storage only. NAND commands (block erase, IDB sector
  writes) are not implemented.
- `capability` stalls on old loaders. Power-cycle the device if later
  commands fail.
- The LBA field is 32 bits: up to 2 TiB.
- `ul` builds the ID block from the loader's FlashData and FlashBoot entries.
  Chips other than RK3128 need `-chip`.

## Formats

| Format                  | Details                                                                                |
| ----------------------- | -------------------------------------------------------------------------------------- |
| Maskrom download        | Vendor request `0x0C`, index `0x471`/`0x472`, 4 KiB chunks, CRC-16/CCITT trailer       |
| Loader commands         | CBW/CSW over bulk. LBA and count are big-endian in the command block                   |
| Loader file (`BOOT`)    | 471/472 entries: one RC4 stream. Flash entries: RC4 per 512 bytes. RK CRC32 at the end |
| ID block                | Header RC4-encrypted, init code at sector 4 (see U-Boot `tools/rkcommon.c`)            |
| `uboot.img`/`trust.img` | 2 KiB header: load address, size, RK CRC32, SHA-256/SHA-1; 4 copies of 1 MiB           |
| Parameter (`PARM`)      | `PARM`, length, text, RK CRC32; copies every 1024 sectors                              |
| RK CRC32                | Polynomial `0x04C10DB7`, init 0, MSB first                                             |

## Tests

`make test` runs:

- Format tests against reference files made by U-Boot `mkimage` and rkbin
  `loaderimage` from synthetic data.
- GPT checks with `sfdisk --verify`, when installed.
- End-to-end CLI tests against a simulated device (`internal/sim`).

Optional tests with real Rockchip files run when these variables are set:

| Variable | Value |
|---|---|
| `RKFLASH_TEST_RKBIN` | Checkout of `rockchip-linux/rkbin` (RK3128 loader and blobs) |
| `RKFLASH_TEST_IMAGES` | Directory with real `uboot.img`/`trust.img` files |
| `RKFLASH_TEST_DISK` | Disk image with a PARM block at sector 0 |

## Layout

```
cmd/rkflash             CLI (main package, commands, tests)
internal/usbfs          Linux usbfs access
internal/rockusb        Loader and Maskrom protocols
internal/rkimage        Loader, ID block, uboot.img, parameter formats
internal/part           GPT, MBR, parameter tables
internal/sim            Simulated device for tests
internal/logx           Leveled logging
testdata                Reference images for tests
```

## License

Copyright 2026 Jeeva Kandasamy.

Licensed under the [Apache License 2.0](LICENSE).

Protocol and format details follow the behavior of Rockchip
[rkdeveloptool](https://github.com/rockchip-linux/rkdeveloptool) and
[U-Boot](https://source.denx.de/u-boot/u-boot) `tools/rkcommon.c`.
rkflash reimplements them and contains no code from either project.
