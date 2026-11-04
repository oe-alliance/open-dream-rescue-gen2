//go:build dm520

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const modelNumber = "520"
const rootDev = "/dev/ubi0_0"
const rootFS = "ubifs"
const dataDev = ""
const rootLabel = "dreambox-rootfs"
const imageKernelPrefix = "boot/vmlinux.gz-"
const rescueLimit = 13 << 20
const rescueOffset = 3 << 20
const rescueSys = "/sys/class/mtd/mtd3"
const rescuePart = "spi0.0"
const rescueReadDev = "/dev/mtd3ro"
const rescueMinor = 7
const nandBlock = 128 << 10
const kernelLimit = 5 << 20
const rootMTD = "/dev/mtd0"
const kernelMTD = "/dev/mtd2"

// /run normally has a half-RAM tmpfs limit, smaller than the OTA ZIP.
// Keep one compressed download, and stream rootfs.bin from it to ubiformat.
func prepareWorkspace() error {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && f[1] == runDir {
			if f[2] != "tmpfs" {
				return errors.New("NAND staging must be in RAM")
			}
			return nil
		}
	}
	return syscall.Mount("tmpfs", runDir, "tmpfs", syscall.MS_NODEV|syscall.MS_NOSUID|syscall.MS_NOEXEC, "size=80%,mode=0700")
}

func layout() (Layout, error) {
	l := Layout{Profile: "Internal NAND", Slots: []Slot{{ID: 1, Device: rootDev}}}
	if !matchesReceiver() {
		return l, errors.New("Unexpected NAND receiver")
	}
	for _, p := range []struct {
		name, label string
		size        uint64
		minor       uint64
	}{
		{"mtd0", "rootfs", 501 << 20, 0}, {"mtd2", "kernel", kernelLimit, 4},
	} {
		d := "/sys/class/mtd/" + p.name + "/"
		st, err := os.Stat("/dev/" + p.name)
		if err != nil || st.Mode()&os.ModeCharDevice == 0 || uint64(st.Sys().(*syscall.Stat_t).Rdev) != 90<<8|p.minor ||
			readText(d+"name") != p.label || readText(d+"type") != "nand" || number(d+"size") != p.size || number(d+"erasesize") != nandBlock || number(d+"writesize") != 2048 {
			return l, errors.New("Unexpected internal NAND geometry")
		}
	}
	return l, nil
}

func kernelBlocks() ([]int64, error) {
	f, err := os.Open("/dev/mtd2ro")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var blocks []int64
	for offset := int64(0); offset < kernelLimit; offset += nandBlock {
		// MEMGETBADBLOCK is _IOW('M',11,__kernel_loff_t) on MIPS.
		bad, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), 0x80084d0b, uintptr(unsafe.Pointer(&offset)))
		if e != 0 {
			return nil, e
		}
		if bad == 0 {
			blocks = append(blocks, offset)
		}
	}
	if len(blocks) == 0 {
		return nil, errors.New("No usable NAND kernel blocks")
	}
	return blocks, nil
}

func nandBootable() bool {
	b, err := kernelBlocks()
	if err != nil {
		return false
	}
	f, err := os.Open("/dev/mtd2ro")
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [3]byte
	_, err = f.ReadAt(magic[:], b[0])
	return err == nil && magic == [3]byte{31, 139, 8}
}

func validImageKernel(data []byte) bool {
	if len(data) < 64 || len(data) > kernelLimit {
		return false
	}
	g, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return false
	}
	defer g.Close()
	const limit = 32 << 20
	k, err := io.ReadAll(io.LimitReader(g, limit+1))
	return err == nil && len(k) >= 52 && len(k) <= limit && bytes.HasPrefix(k, []byte("\x7fELF\x01\x01\x01")) &&
		binary.LittleEndian.Uint16(k[16:]) == 2 && binary.LittleEndian.Uint16(k[18:]) == 8 && imageModelBanner.Match(k)
}

func readNANDKernel() ([]byte, error) {
	blocks, err := kernelBlocks()
	if err != nil {
		return nil, err
	}
	f, err := os.Open("/dev/mtd2ro")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data := make([]byte, len(blocks)*nandBlock)
	for i, off := range blocks {
		if _, err := f.ReadAt(data[i*nandBlock:(i+1)*nandBlock], off); err != nil {
			return nil, err
		}
	}
	b := bytes.NewReader(data)
	g, err := gzip.NewReader(b)
	if err != nil {
		return nil, err
	}
	defer g.Close()
	g.Multistream(false)
	if _, err = bounded(io.Discard, g, 32<<20); err != nil {
		return nil, err
	}
	return data[:len(data)-b.Len()], nil
}

func backupKernel(w io.Writer) error {
	k, err := readNANDKernel()
	if err != nil {
		return err
	}
	if !validImageKernel(k) {
		return errors.New("Unexpected internal image kernel")
	}
	_, err = w.Write(k)
	return err
}

func prepareClassicKernel(p Plan) error {
	k, err := os.ReadFile(p.Kernel)
	if err != nil {
		return err
	}
	if !validImageKernel(k) {
		return errors.New("Invalid DM52x NAND kernel")
	}
	blocks, err := kernelBlocks()
	if err != nil {
		return err
	}
	if len(k) > len(blocks)*nandBlock {
		return errors.New("Too few good NAND blocks for this kernel")
	}
	return nil
}

func flashKernel(p Plan) error {
	if err := command("/usr/sbin/nandwrite", "-p", "-m", kernelMTD, p.Kernel); err != nil {
		return err
	}
	got, err := readNANDKernel()
	if err != nil {
		return err
	}
	want, err := os.ReadFile(p.Kernel)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return errors.New("NAND kernel read-back mismatch")
	}
	return nil
}

func checkNANDIdle() error {
	if _, err := layout(); err != nil {
		return err
	}
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return err
	}
	ramRoot := false
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		if f[1] == "/" {
			if f[2] != "rootfs" && f[2] != "ramfs" && f[2] != "tmpfs" {
				return errors.New("Boot Open Dream Rescue from RAM before installing")
			}
			ramRoot = true
		}
		if f[2] == "ubifs" || strings.HasPrefix(f[0], "/dev/mtdblock") {
			return errors.New("Internal flash is mounted")
		}
	}
	if !ramRoot {
		return errors.New("Rescue RAM root not found")
	}
	for _, n := range []string{"0", "2"} {
		st, err := os.Stat("/dev/mtd" + n + "ro")
		minor, _ := strconv.Atoi(n)
		if err != nil || st.Mode()&os.ModeCharDevice == 0 || uint64(st.Sys().(*syscall.Stat_t).Rdev) != uint64(90<<8|2*minor+1) {
			return errors.New("Unexpected NAND read-back device")
		}
	}
	return nil
}

func detachRoot() error {
	attached, err := filepath.Glob("/sys/class/ubi/ubi[0-9]*/mtd_num")
	if err != nil {
		return err
	}
	for _, path := range attached {
		if readText(path) == "0" && filepath.Base(filepath.Dir(path)) != "ubi0" {
			return errors.New("Root NAND attached to another UBI device")
		}
	}
	if _, err := os.Stat("/sys/class/ubi/ubi0"); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if readText("/sys/class/ubi/ubi0/mtd_num") != "0" {
		return errors.New("UBI0 belongs to another device")
	}
	volumes, err := filepath.Glob("/sys/class/ubi/ubi0_*/name")
	if err != nil {
		return err
	}
	for _, v := range volumes {
		if filepath.Base(filepath.Dir(v)) != "ubi0_0" || readText(v) != rootLabel {
			return errors.New("Unexpected volumes on internal NAND")
		}
	}
	f, err := os.OpenFile("/dev/ubi_ctrl", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	var id int32
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), 0x80046f41, uintptr(unsafe.Pointer(&id)))
	if e != 0 {
		return e
	}
	return nil
}

func waitNode(path string) error {
	for i := 0; i < 50; i++ {
		if st, e := os.Stat(path); e == nil && st.Mode()&os.ModeCharDevice != 0 {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("Missing device %s", path)
}

func withUBI(p Plan, fn func(io.Reader, int64) error) error {
	z, err := zip.OpenReader(p.Tar)
	if err != nil {
		return err
	}
	defer z.Close()
	for _, x := range z.File {
		n, err := normal(x.Name)
		if err != nil {
			return err
		}
		if n != model+"/rootfs.bin" {
			continue
		}
		r, err := x.Open()
		if err != nil {
			return err
		}
		defer r.Close()
		prefix := make([]byte, 2*nandBlock)
		if _, err = io.ReadFull(r, prefix); err != nil {
			return err
		}
		for i := 0; i < 2; i++ {
			b := prefix[i*nandBlock:]
			h := b[:64]
			v := b[2048:2112]
			vol := b[4096 : 4096+172]
			if string(h[:5]) != "UBI#\x01" || binary.BigEndian.Uint32(h[16:]) != 2048 || binary.BigEndian.Uint32(h[20:]) != 4096 ||
				binary.BigEndian.Uint32(h[60:]) != ^crc32.ChecksumIEEE(h[:60]) || string(v[:5]) != "UBI!\x01" || binary.BigEndian.Uint32(v[8:]) != 0x7fffefff ||
				binary.BigEndian.Uint32(v[12:]) != uint32(i) || binary.BigEndian.Uint32(v[60:]) != ^crc32.ChecksumIEEE(v[:60]) ||
				binary.BigEndian.Uint16(vol[14:]) != uint16(len(rootLabel)) || string(vol[16:16+len(rootLabel)]) != rootLabel || binary.BigEndian.Uint32(vol[168:]) != ^crc32.ChecksumIEEE(vol[:168]) {
				return errors.New("Invalid DM52x UBI geometry or root volume table")
			}
		}
		return fn(io.MultiReader(bytes.NewReader(prefix), r), int64(x.UncompressedSize64))
	}
	return errors.New("Missing NAND root image")
}

// A legacy root tarball has no separate kernel. Check the model-matched
// /boot entry before erasing NAND, stopping as soon as that entry is read.
func locateNANDKernel(p Plan) error {
	if !p.RootKernel {
		return nil
	}
	if err := os.Remove(p.Kernel); err != nil && !os.IsNotExist(err) {
		return err
	}
	found := errors.New("kernel located")
	err := withTar(p.Tar, func(r io.Reader) error {
		tr := tar.NewReader(io.LimitReader(r, maxRoot))
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return errors.New("Missing DM52x kernel in root filesystem")
			}
			if err != nil {
				return err
			}
			n, err := normal(h.Name)
			if err != nil {
				return err
			}
			if h.Typeflag == tar.TypeReg && strings.HasPrefix(n, imageKernelPrefix) && strings.HasSuffix(n, "-"+model) {
				if err := save(p.Kernel, tr, kernelLimit); err != nil {
					return err
				}
				return found
			}
		}
	})
	if errors.Is(err, found) {
		return nil
	}
	return err
}

// The compressed upload shares physical RAM with UBIFS and the extractor.
// Release whole tmpfs pages only after their input bytes have been copied.
type nandTarInput struct {
	*os.File
	next, released int64
}

func (in *nandTarInput) ReadAt(b []byte, offset int64) (int, error) {
	if offset < in.released {
		return 0, errors.New("Unexpected backward read in the NAND image")
	}
	n, err := in.File.ReadAt(b, offset)
	if offset != in.next || n == 0 {
		return n, err
	}
	in.next += int64(n)
	const batch = 1 << 20
	end := in.next / batch * batch
	if end > in.released {
		pages, e := syscall.Mmap(int(in.Fd()), in.released, int(end-in.released), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
		if e != nil {
			return 0, e
		}
		e = syscall.Madvise(pages, syscall.MADV_REMOVE)
		unmap := syscall.Munmap(pages)
		if e != nil || unmap != nil {
			return 0, fmt.Errorf("Releasing consumed rescue RAM: %w", errors.Join(e, unmap))
		}
		in.released = end
	}
	return n, err
}

func consumeNANDTar(p Plan, dst io.Writer) error {
	if filepath.Dir(p.Directory) != runDir || p.Tar != filepath.Join(p.Directory, "input") {
		return errors.New("Expected a private staged NAND image")
	}
	f, err := os.OpenFile(p.Tar, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	var fs syscall.Statfs_t
	if err := syscall.Fstatfs(int(f.Fd()), &fs); err != nil {
		return err
	}
	if fs.Type != 0x01021994 || !st.Mode().IsRegular() || st.Sys().(*syscall.Stat_t).Nlink != 1 {
		return errors.New("Expected a private regular file in rescue tmpfs")
	}
	in := &nandTarInput{File: f, next: -1}
	return readTarAt(in, st.Size(), func(offset int64) { in.next = offset }, func(r io.Reader) error {
		return p.checkTar(r, dst, maxRoot)
	})
}

func install(p Plan, l Layout, s Slot) error {
	if l.Multi || s.ID != 1 || s.Folder != "" || s.Device != rootDev {
		return errors.New("Select the internal NAND image")
	}
	if err := checkNANDIdle(); err != nil {
		return err
	}
	if err := locateNANDKernel(p); err != nil {
		return err
	}
	if err := prepareClassicKernel(p); err != nil {
		return err
	}
	erase := func() error {
		if err := detachRoot(); err != nil {
			return err
		}
		erasingDestination("Replacing the internal NAND image")
		return command("/usr/sbin/flash_erase", kernelMTD, "0", "0")
	}
	if p.UBI {
		err := withUBI(p, func(input io.Reader, size int64) error {
			if err := erase(); err != nil {
				return err
			}
			job("working", "Writing the UBI image to internal NAND")
			c := exec.Command("/usr/sbin/ubiformat", rootMTD, "-y", "-q", "-f", "-", "-S", strconv.FormatInt(size, 10))
			c.Stdin = input
			if out, err := c.CombinedOutput(); err != nil {
				return fmt.Errorf("Writing UBI image: %w %.300s", err, out)
			}
			return nil
		})
		if err != nil {
			return err
		}
	} else {
		if err := erase(); err != nil {
			return err
		}
		if err := command("/usr/sbin/ubiformat", rootMTD, "-y", "-q"); err != nil {
			return err
		}
	}
	if err := command("/usr/sbin/ubiattach", "-d", "0", "-p", rootMTD); err != nil {
		return err
	}
	if err := waitNode("/dev/ubi0"); err != nil {
		return err
	}
	if readText("/sys/class/ubi/ubi0/mtd_num") != "0" {
		return errors.New("Unexpected UBI attachment")
	}
	if !p.UBI {
		if err := command("/usr/sbin/ubimkvol", "/dev/ubi0", "-n", "0", "-N", rootLabel, "-m"); err != nil {
			return err
		}
	}
	if err := waitNode(rootDev); err != nil {
		return err
	}
	if readText("/sys/class/ubi/ubi0_0/name") != rootLabel {
		return errors.New("Expected the internal dreambox-rootfs volume")
	}
	// UBIFS initializes a newly created empty volume on its first writable mount.
	return mounted(rootDev, rootFS, true, func(dir string) error {
		if err := markIncomplete(dir, s); err != nil {
			return err
		}
		if !p.UBI {
			job("working", "Extracting the image directly to internal NAND")
			// UBIFS compresses file data. Only the filesystem can determine when the NAND is full.
			if err := extractTo(dir, func(w io.Writer) error { return consumeNANDTar(p, w) }); err != nil {
				return err
			}
			if p.RootKernel {
				if err := prepareClassicKernel(p); err != nil {
					return err
				}
			}
		}
		if !installed(dir) {
			return errors.New("The NAND root filesystem is incomplete")
		}
		syscall.Sync()
		job("working", "Writing and verifying the internal NAND kernel")
		if err := flashKernel(p); err != nil {
			return err
		}
		return os.Remove(installMarker(dir, s))
	})
}
