//go:build dm7080 || dm820

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const rootFS = "ext4"
const rescueOffset = 0
const rootDev = "/dev/mmcblk0p1"
const dataDev = "/dev/mmcblk0p2"
const imageKernelPrefix = "boot/vmlinux.bin-"
const rescueLimit = 15 << 20
const rescueSys = "/sys/class/mtd/mtd2"
const rescuePart = "recovery"
const rescueReadDev = "/dev/mtd2ro"
const rescueMinor = 5
const rootLabel = "dreambox-rootfs"
const bankTool = "/usr/sbin/odr-bank"

func validImageKernel(b []byte) bool {
	if len(b) < 4096 || len(b) > maxKernel || bytes.HasPrefix(b, []byte("\x7fELF")) || bytes.HasPrefix(b, []byte{31, 139, 8}) {
		return false
	}
	firstType := binary.LittleEndian.Uint32(b[12:])
	return !(binary.LittleEndian.Uint32(b[4:]) == 1 && (firstType == 1 || firstType == 8)) && imageModelBanner.Match(b)
}

func checkBankA() error {
	const firstRootSector = 147456
	total := number("/sys/class/block/mmcblk0/size")
	if readText("/proc/stb/info/model") != model || readText("/proc/stb/info/mid") != modelMID ||
		number("/sys/class/block/mmcblk0/queue/logical_block_size") != 512 ||
		number("/sys/class/block/mmcblk0p1/start") != firstRootSector || total <= firstRootSector {
		return errors.New("Unrecognised " + modelName + " eMMC layout")
	}
	parts, err := filepath.Glob("/sys/class/block/mmcblk0p[0-9]*")
	if err != nil || len(parts) == 0 {
		return errors.New("Missing eMMC partition information")
	}
	for _, part := range parts {
		start, size := number(part+"/start"), number(part+"/size")
		if start < firstRootSector || start >= total || size == 0 || size > total-start {
			return errors.New("An eMMC partition overlaps the kernel banks or exceeds the disk")
		}
	}
	f, err := os.Open("/dev/mmcblk0")
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Mode()&(os.ModeDevice|os.ModeCharDevice) != os.ModeDevice || st.Sys().(*syscall.Stat_t).Rdev != 179<<8 {
		return errors.New("Unexpected eMMC device")
	}
	var selector [5]byte
	if _, err = f.ReadAt(selector[:], 64*512); err != nil || string(selector[:]) != "optA\x00" {
		return errors.New("The internal kernel selector is not bank A")
	}
	return nil
}

func prepareClassicKernel(p Plan) error {
	if err := checkBankA(); err != nil {
		return err
	}
	return command(bankTool, "prepare", p.Kernel, filepath.Join(p.Directory, "bank-a"))
}

func flashKernel(p Plan) error {
	_, err := runBank(os.O_RDWR, "write", filepath.Join(p.Directory, "bank-a"))
	return err
}

func runBank(flags int, args ...string) ([]byte, error) {
	if err := checkBankA(); err != nil {
		return nil, err
	}
	disk, err := os.OpenFile("/dev/mmcblk0", flags|syscall.O_DIRECT, 0)
	if err != nil {
		return nil, err
	}
	defer disk.Close()
	cmd := exec.Command(bankTool, args...)
	cmd.ExtraFiles = []*os.File{disk}
	return cmd.Output()
}

func backupKernel(w io.Writer) error {
	b, err := runBank(os.O_RDONLY, "read")
	if err != nil {
		return fmt.Errorf("Reading kernel bank A: %w", err)
	}
	if !validImageKernel(b) {
		return errors.New("Bank A does not contain a " + modelName + " image kernel")
	}
	_, err = w.Write(b)
	return err
}
