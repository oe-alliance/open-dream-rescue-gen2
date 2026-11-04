//go:build !dm7080 && !dm820 && !dm520

package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

const rootFS = "ext4"
const rescueOffset = 0
const rootDev = "/dev/mmcblk0p2"
const dataDev = "/dev/mmcblk0p3"
const imageKernelPrefix = "boot/zImage-"
const rescueLimit = 14417920
const rescueSys = "/sys/class/mtd/mtd3"
const rescuePart = "flash0.recovery"
const rescueReadDev = "/dev/mtd3ro"
const rescueMinor = 7
const rootLabel = "rootfs"

func validImageKernel(data []byte) bool { return armKernel(data) }
func prepareClassicKernel(p Plan) error {
	if err := idleDevice(p1); err != nil {
		return err
	}
	if number("/sys/class/block/mmcblk0p1/size") != 63488 {
		return errors.New("Unexpected kernel partition geometry")
	}
	return nil
}

func flashKernel(p Plan) error {
	kernel, e := os.ReadFile(p.Kernel)
	if e != nil || !armKernel(kernel) {
		return errors.New("Invalid prepared kernel")
	}
	f, e := os.OpenFile(p1, os.O_RDWR, 0)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = f.WriteAt(kernel, 0); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	verify := make([]byte, len(kernel))
	if _, e = f.ReadAt(verify, 0); e != nil || !bytes.Equal(kernel, verify) {
		return errors.New("Kernel read-back failed")
	}
	return nil
}
func backupKernel(w io.Writer) error {
	f, err := os.Open(p1)
	if err != nil {
		return err
	}
	defer f.Close()
	var header [48]byte
	if _, err = io.ReadFull(f, header[:]); err != nil {
		return err
	}
	start, end := binary.LittleEndian.Uint32(header[40:]), binary.LittleEndian.Uint32(header[44:])
	if binary.LittleEndian.Uint32(header[36:]) != 0x016f2818 || end <= start || end-start < 48 || end-start > maxKernel {
		return errors.New("The internal kernel is not a supported " + modelName + " zImage")
	}
	if _, err = w.Write(header[:]); err != nil {
		return err
	}
	_, err = io.CopyN(w, f, int64(end-start)-48)
	return err
}
