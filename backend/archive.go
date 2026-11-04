package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"syscall"
)

const maxUpload = int64(640 << 20)
const maxKernel = 31 << 20
const maxRoot = int64(12 << 30)

type Plan struct {
	Directory, Tar, Kernel string
	Expanded               int64
	RootKernel             bool
	UBI                    bool
	inspection             *slotInspection
}

func armKernel(b []byte) bool {
	if len(b) < 48 || len(b) > maxKernel || binary.LittleEndian.Uint32(b[36:]) != 0x016f2818 {
		return false
	}
	start, end := binary.LittleEndian.Uint32(b[40:]), binary.LittleEndian.Uint32(b[44:])
	return end > start && uint64(end-start) == uint64(len(b))
}
func normal(n string) (string, error) {
	for strings.HasPrefix(n, "./") {
		n = n[2:]
	}
	n = strings.TrimSuffix(n, "/")
	if n == "" || n == "." {
		return ".", nil
	}
	if path.Clean(n) != n || strings.HasPrefix(n, "/") || n == ".." || strings.HasPrefix(n, "../") || strings.ContainsAny(n, "\\\x00\r\n") {
		return "", errors.New("Unsafe archive path")
	}
	return n, nil
}
func bounded(dst io.Writer, src io.Reader, max int64) (int64, error) {
	n, e := io.Copy(dst, io.LimitReader(src, max+1))
	if e == nil && n > max {
		e = errors.New("Image exceeds size limit")
	}
	return n, e
}
func save(path string, r io.Reader, limit int64) error {
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = bounded(f, r, limit)
	cl := f.Close()
	if e != nil {
		return e
	}
	return cl
}
func withTar(file string, fn func(io.Reader) error) error {
	f, e := os.Open(file)
	if e != nil {
		return e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return e
	}
	return readTarAt(f, st.Size(), nil, fn)
}

// Payload reads are sequential after ZIP directory and local header lookup.
func readTarAt(f io.ReaderAt, size int64, begin func(int64), fn func(io.Reader) error) error {
	var sig [4]byte
	if _, err := f.ReadAt(sig[:], 0); err != nil {
		return err
	}
	if string(sig[:]) != "PK\x03\x04" {
		if begin != nil {
			begin(0)
		}
		return unpackStream(bufio.NewReader(io.NewSectionReader(f, 0, size)), fn)
	}
	z, e := zip.NewReader(f, size)
	if e != nil {
		return e
	}
	for _, x := range z.File {
		n, e := normal(x.Name)
		if e != nil {
			return e
		}
		if rootMember(n) {
			in, e := x.Open()
			if e != nil {
				return e
			}
			defer in.Close()
			if begin != nil {
				offset, err := x.DataOffset()
				if err != nil {
					return err
				}
				begin(offset)
			}
			if err := unpackStream(bufio.NewReader(in), fn); err != nil {
				return err
			}
			_, err := io.Copy(io.Discard, in) // Verify the ZIP member CRC through EOF.
			return err
		}
	}
	return errors.New("ZIP root filesystem missing")
}
func rootMember(n string) bool {
	if nandImage && webTar(n) {
		return true
	}
	return n == model+"/rootfs.tar.bz2" || n == model+"/rootfs.tar.xz" || n == model+"/rootfs.tar.gz" || n == model+"/rootfs.tar"
}

// OpenATV DM52x web ZIPs put the named tarball at the ZIP root.
func webTar(n string) bool {
	return !strings.Contains(n, "/") && strings.HasSuffix(n, ".rootfs.tar.xz") && strings.Contains(n, "-dm520-")
}
func unpackStream(src *bufio.Reader, fn func(io.Reader) error) error {
	sig, e := src.Peek(6)
	if e != nil {
		return e
	}
	process := func(r io.Reader) error {
		if e := fn(r); e != nil {
			return e
		}
		n, e := bounded(io.Discard, r, 8<<20)
		if n > 8<<20 {
			return errors.New("Unexpected data after tar archive")
		}
		return e
	}
	switch {
	case bytes.HasPrefix(sig, []byte("BZh")):
		return process(bzip2.NewReader(src))
	case bytes.HasPrefix(sig, []byte{31, 139, 8}):
		g, e := gzip.NewReader(src)
		if e != nil {
			return e
		}
		defer g.Close()
		return process(g)
	case bytes.Equal(sig, []byte{0xfd, '7', 'z', 'X', 'Z', 0}):
		c := exec.Command("xz", "-dc")
		c.Stdin = src
		r, e := c.StdoutPipe()
		if e != nil {
			return e
		}
		if e = c.Start(); e != nil {
			return e
		}
		e = process(r)
		r.Close()
		w := c.Wait()
		if e != nil {
			return e
		}
		return w
	default:
		return process(src)
	}
}
func openImage(dir, file string) (Plan, error) {
	p := Plan{Directory: dir, Tar: file, Kernel: filepath.Join(dir, "kernel")}
	f, e := os.Open(file)
	if e != nil {
		return p, e
	}
	sig, e := io.ReadAll(io.LimitReader(f, 512))
	f.Close()
	if e != nil {
		return p, e
	}
	packaged := bytes.HasPrefix(sig, []byte("PK\x03\x04"))
	if !packaged {
		if e := rootHeader(sig); e != nil {
			return p, e
		}
		p.RootKernel = true
		return p, nil
	}
	z, e := zip.OpenReader(file)
	if e != nil {
		return p, e
	}
	defer z.Close()
	found := false
	for _, x := range z.File {
		n, e := normal(x.Name)
		if e != nil {
			return p, e
		}
		if x.FileInfo().IsDir() {
			continue
		}
		target := ""
		limit := maxUpload
		switch {
		case n == model+"/kernel.bin":
			target = p.Kernel
			limit = maxKernel
		case nandImage && n == model+"/rootfs.bin":
			if found || x.UncompressedSize64 < 3*131072 || x.UncompressedSize64 > 501<<20 || x.UncompressedSize64%131072 != 0 {
				return p, errors.New("Invalid or duplicate NAND root image")
			}
			p.UBI, found = true, true
			continue
		case rootMember(n):
			if nandImage && webTar(n) {
				p.RootKernel = true
			}
			if found || x.UncompressedSize64 > uint64(maxUpload) {
				return p, errors.New("Invalid or duplicate ZIP rootfs")
			}
			found = true
			r, err := x.Open()
			if err != nil {
				return p, err
			}
			sig, err = io.ReadAll(io.LimitReader(r, 512))
			// ODR stores the compressed rootfs verbatim. Check its ZIP CRC in RAM
			// before offering a destination, without decompressing the filesystem.
			if err == nil && x.Method == zip.Store && strings.HasSuffix(n, "/rootfs.tar.gz") {
				_, err = io.Copy(io.Discard, r)
				if errors.Is(err, zip.ErrChecksum) {
					err = errors.New("The backup ZIP contains a damaged root filesystem (CRC mismatch). Choose another copy before installing.")
				}
			}
			r.Close()
			if err != nil {
				return p, err
			}
			if err = rootHeader(sig); err != nil {
				return p, err
			}
			continue
		default:
			continue
		}
		if x.UncompressedSize64 > uint64(limit) {
			return p, errors.New("ZIP member too large")
		}
		r, e := x.Open()
		if e != nil {
			return p, e
		}
		e = save(target, r, limit)
		r.Close()
		if e != nil {
			return p, e
		}
	}
	if !found {
		return p, errors.New("The ZIP must contain " + model + "/rootfs.tar.*")
	}
	if nandImage && p.RootKernel {
		if _, err := os.Lstat(p.Kernel); !os.IsNotExist(err) {
			return p, errors.New("Ambiguous image kernel")
		}
		return p, nil
	}
	if bankedKernel {
		f, err := os.Open(p.Kernel)
		if err != nil {
			return p, err
		}
		marker, err := io.ReadAll(io.LimitReader(f, 32))
		f.Close()
		if err != nil {
			return p, err
		}
		if string(marker) == "dummy file dont delete\n" {
			if err := os.Remove(p.Kernel); err != nil {
				return p, err
			}
			p.RootKernel = true
			return p, nil
		}
	}
	k, e := os.ReadFile(p.Kernel)
	if e != nil || !validImageKernel(k) {
		return p, errors.New("Missing or invalid " + modelName + " image kernel")
	}
	return p, nil
}

func rootHeader(sig []byte) error {
	if bytes.HasPrefix(sig, []byte("BZh")) || bytes.HasPrefix(sig, []byte{31, 139, 8}) || bytes.HasPrefix(sig, []byte{0xfd, '7', 'z', 'X', 'Z', 0}) {
		return nil
	}
	if len(sig) == 512 && bytes.Equal(sig[257:262], []byte("ustar")) {
		return nil
	}
	if _, err := tar.NewReader(bytes.NewReader(sig)).Next(); err != nil {
		return errors.New("Unrecognised root filesystem archive")
	}
	return nil
}

// Track the installation limits and required files as TAR entries pass through.
type rootArchive struct {
	size           int64
	entries        int
	init, ldconfig bool
}

func (a *rootArchive) entry(h *tar.Header, name string, free int64) error {
	if a.entries > 300000 {
		return errors.New("Too many archive entries")
	}
	if h.Size < 0 || h.Size > maxRoot-a.size {
		return errors.New("Expanded image too large")
	}
	if h.Size > free-a.size {
		return errors.New("Insufficient space for this image")
	}
	switch h.Typeflag {
	case tar.TypeReg, tar.TypeRegA, tar.TypeSymlink, tar.TypeLink:
		if name == "sbin/init" {
			a.init = true
		}
		if name == "sbin/ldconfig" {
			a.ldconfig = true
		}
	case tar.TypeDir, tar.TypeFifo, tar.TypeChar, tar.TypeBlock:
	default:
		return errors.New("Unsupported root filesystem archive entry")
	}
	a.entries++
	a.size += h.Size
	return nil
}

// Verify the same stream that is sent to the extractor or classic-image cache.
func (p *Plan) checkRoot(dst io.Writer, free int64) error {
	return withTar(p.Tar, func(r io.Reader) error { return p.checkTar(r, dst, free) })
}

func (p *Plan) checkTar(r io.Reader, dst io.Writer, free int64) error {
	p.Expanded = 0
	if p.RootKernel {
		if err := os.Remove(p.Kernel); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	hasModel := !p.RootKernel
	var contents rootArchive
	var buffer *bufio.Writer
	if dst != nil {
		buffer = bufio.NewWriterSize(dst, 64<<10)
		r = io.TeeReader(r, buffer)
	}
	tr := tar.NewReader(r)
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		n, e := normal(h.Name)
		if e != nil {
			return e
		}
		if strings.HasPrefix(n, ".odr-incomplete-") {
			return errors.New("Reserved installer marker in image")
		}
		if e = contents.entry(h, n, free); e != nil {
			return e
		}
		if p.inspection != nil {
			if e = p.inspection.observe(tr, h, n); e != nil {
				return e
			}
		}
		if h.Typeflag == tar.TypeLink {
			if _, e = normal(h.Linkname); e != nil {
				return e
			}
		}
		if p.RootKernel && h.Typeflag == tar.TypeReg && strings.HasPrefix(n, imageKernelPrefix) && strings.HasSuffix(n, "-"+model) {
			hasModel = true
			if e = save(p.Kernel, tr, maxKernel); e != nil {
				return e
			}
		}
	}
	if buffer != nil {
		if err := buffer.Flush(); err != nil {
			return err
		}
	}

	p.Expanded = contents.size
	if !hasModel || !contents.init || !contents.ldconfig {
		return errors.New("Missing " + modelName + " image identity or bootable root filesystem")
	}
	if p.RootKernel {
		k, err := os.ReadFile(p.Kernel)
		if err != nil || !validImageKernel(k) {
			return errors.New("Missing or invalid " + modelName + " image kernel")
		}
	}
	return nil
}

func prepare(dir, file string) (Plan, error) {
	p, err := openImage(dir, file)
	if err == nil {
		err = p.checkRoot(nil, maxRoot)
	}
	return p, err
}

// The child is confined to an empty directory and never runs image scripts.
func extractTo(dir string, stream func(io.Writer) error) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command(exe, "extract", dir)
	var message bytes.Buffer
	c.Stderr = &message
	in, err := c.StdinPipe()
	if err != nil {
		return err
	}
	if err = c.Start(); err != nil {
		in.Close()
		return err
	}
	err = stream(in)
	in.Close()
	wait := c.Wait()
	if wait != nil && (err == nil || errors.Is(err, syscall.EPIPE)) {
		return fmt.Errorf("Extracting image: %w %.300s", wait, message.Bytes())
	}
	return err
}
func permissions(h *tar.Header) os.FileMode {
	return h.FileInfo().Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
}
func attrs(h *tar.Header, n string) error {
	if e := os.Lchown(n, h.Uid, h.Gid); e != nil {
		return e
	}
	if h.Typeflag == tar.TypeSymlink {
		return nil
	}
	if e := os.Chmod(n, permissions(h)); e != nil {
		return e
	}
	for k, v := range h.PAXRecords {
		if strings.HasPrefix(k, "SCHILY.xattr.") {
			if e := syscall.Setxattr(n, k[len("SCHILY.xattr."):], []byte(v), 0); e != nil {
				return e
			}
		}
	}
	if !h.ModTime.IsZero() {
		return os.Chtimes(n, h.ModTime, h.ModTime)
	}
	return nil
}
func extractChild(dir string) error {
	items, e := os.ReadDir(dir)
	if e != nil {
		return e
	}
	// A freshly formatted classic root contains only ODR's slot 1 marker.
	if len(items) != 0 && !(len(items) == 1 && items[0].Name() == ".odr-incomplete-1" && items[0].Type().IsRegular()) {
		return errors.New("Destination must be empty")
	}
	if e = syscall.Chroot(dir); e != nil {
		return e
	}
	if e = os.Chdir("/"); e != nil {
		return e
	}
	tr := tar.NewReader(os.Stdin)
	var dirs []*tar.Header
	var links []*tar.Header
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		n, e := normal(h.Name)
		if e != nil {
			return e
		}
		h.Name = n
		if n == "." {
			continue
		}
		if e = os.MkdirAll(path.Dir(n), 0755); e != nil {
			return e
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if e = os.MkdirAll(n, 0755); e != nil {
				return e
			}
			dirs = append(dirs, h)
			continue
		case tar.TypeReg, tar.TypeRegA:
			f, e := os.OpenFile(n, os.O_WRONLY|os.O_EXCL|os.O_CREATE, 0600)
			if e != nil {
				return e
			}
			_, e = io.CopyN(f, tr, h.Size)
			ce := f.Close()
			if e != nil {
				return e
			}
			if ce != nil {
				return ce
			}
		case tar.TypeSymlink:
			if e = os.Symlink(h.Linkname, n); e != nil {
				return e
			}
		case tar.TypeLink:
			links = append(links, h)
			continue
		case tar.TypeFifo, tar.TypeChar, tar.TypeBlock:
			mode := uint32(syscall.S_IFIFO)
			if h.Typeflag == tar.TypeChar {
				mode = syscall.S_IFCHR
			}
			if h.Typeflag == tar.TypeBlock {
				mode = syscall.S_IFBLK
			}
			maj, min := uint64(h.Devmajor), uint64(h.Devminor)
			dev := int((min & 255) | ((maj & 4095) << 8) | ((min &^ 255) << 12))
			if e = syscall.Mknod(n, mode|0600, dev); e != nil {
				return e
			}
		default:
			return fmt.Errorf("Unsupported tar entry %q, type %d", n, h.Typeflag)
		}
		if e = attrs(h, n); e != nil {
			return e
		}
	}
	for _, h := range links {
		src, e := normal(h.Linkname)
		if e != nil {
			return e
		}
		st, e := os.Lstat(src)
		if e != nil || !st.Mode().IsRegular() {
			return errors.New("Invalid hard link")
		}
		if e = os.Link(src, h.Name); e != nil {
			return e
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if e := attrs(dirs[i], dirs[i].Name); e != nil {
			return e
		}
	}
	return nil
}
