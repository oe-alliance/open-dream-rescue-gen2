package main

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// linux/loop.h, identical layout on ARM EABI and 64-bit Linux.
type loopInfo64 struct {
	Device, Inode, RDevice, Offset, SizeLimit uint64
	Number, Crypt, KeySize, Flags             uint32
	FileName, CryptName                       [64]byte
	Key                                       [32]byte
	Init                                      [2]uint64
}

func readOnlyLoop(path string) (string, func(), error) {
	back, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, err
	}
	defer syscall.Close(back)
	ctl, err := syscall.Open("/dev/loop-control", syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", nil, err
	}
	defer syscall.Close(ctl)
	for attempt := 0; attempt < 4; attempt++ {
		number, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(ctl), 0x4c82, 0) // LOOP_CTL_GET_FREE
		if e != 0 {
			return "", nil, e
		}
		name := fmt.Sprintf("/dev/loop%d", number)
		fd, err := syscall.Open(name, syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		if err != nil {
			return "", nil, err
		}
		_, _, e = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), 0x4c00, uintptr(back)) // LOOP_SET_FD
		if e != 0 {
			syscall.Close(fd)
			if e == syscall.EBUSY {
				continue
			}
			return "", nil, e
		}
		st := loopInfo64{Flags: 4} // LO_FLAGS_AUTOCLEAR on last close
		_, _, e = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), 0x4c04, uintptr(unsafe.Pointer(&st)))
		if e != 0 {
			syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), 0x4c01, 0)
			syscall.Close(fd)
			return "", nil, e
		}
		var ro int32
		blkROGet := uintptr(0x125e)
		if runtime.GOARCH == "mipsle" {
			blkROGet |= 0x20000000 // MIPS _IOC_NONE is 1.
		}
		_, _, e = syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), blkROGet, uintptr(unsafe.Pointer(&ro)))
		if e != 0 || ro != 1 {
			syscall.Close(fd)
			return "", nil, fmt.Errorf("Cannot verify read-only loop for %s", path)
		}
		return name, func() { syscall.Close(fd) }, nil
	}
	return "", nil, syscall.EBUSY
}
