package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

var imageModelBanner = regexp.MustCompile(`Linux version [A-Za-z0-9._+-]+-` + model + ` \(`)

// The OE Dream kernel recipes embed these exact chkroot-multiboot.cpio.xz
// archives. Their /init reads STARTUP and boots the requested rootsubdir.
type slotInspection struct {
	osID, usrID string
	features    map[string]string
	drivers     map[string]bool
	top         map[string]bool
}

func (s *slotInspection) observe(r io.Reader, h *tar.Header, n string) error {
	if n != "." {
		if s.top == nil {
			s.top = make(map[string]bool)
		}
		name, _, _ := strings.Cut(n, "/")
		if !s.top[name] && len(s.top) >= 1024 {
			return errors.New("Too many top-level image entries")
		}
		s.top[name] = true
	}
	if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
		return nil
	}
	switch n {
	case "etc/os-release", "usr/lib/os-release", "usr/lib/enigma.info":
		if h.Size > 32768 {
			return nil
		}
		b, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		props := imageProperties(b)
		switch n {
		case "etc/os-release":
			s.osID = props["ID"]
		case "usr/lib/os-release":
			s.usrID = props["ID"]
		default:
			s.features = props
		}
	default:
		n = strings.TrimPrefix(n, "usr/")
		if h.Size > 0 && strings.HasPrefix(n, "lib/modules/") && (strings.HasSuffix(n, "/extra/bcm7439.ko") || strings.HasSuffix(n, "/extra/bcm7435.ko")) {
			if s.drivers == nil {
				s.drivers = make(map[string]bool)
			}
			s.drivers[n] = true
		}
	}
	return nil
}

func sharedKernelStatus(file string, image *slotInspection) (string, error) {
	k, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	size := 1523472
	hash := "16d13c86ea597649b9001e3af0756582b4989312f1589dfc413088363b557dd3"
	if !bankedKernel {
		if !armKernel(k) {
			return "", errors.New("Invalid image kernel")
		}
		offset := bytes.Index(k, []byte{31, 139, 8})
		if offset < 0 {
			return "unverified", nil
		}
		g, err := gzip.NewReader(bytes.NewReader(k[offset:]))
		if err != nil {
			return "unverified", nil
		}
		defer g.Close()
		g.Multistream(false)
		const limit = 48 << 20
		k, err = io.ReadAll(io.LimitReader(g, limit+1))
		if err != nil || len(k) > limit {
			return "unverified", nil
		}
		size = 1331032
		hash = "81b75482e8077a779af1129e5e102a2fa65eb13e85446c6f781bbf258920cfd6"
	}
	banner := imageModelBanner.FindIndex(k)
	if banner == nil {
		return "unverified", nil
	}
	osID := strings.ToLower(image.osID)
	if osID == "" {
		osID = strings.ToLower(image.usrID)
	}
	if osID == "opendreambox" || osID == "dreamos" {
		return "legacy", nil
	}
	release := strings.Fields(string(k[banner[0]:banner[1]]))[2]
	chip := "bcm7439"
	if bankedKernel {
		chip = "bcm7435"
	}
	if !image.drivers["lib/modules/"+release+"/extra/"+chip+".ko"] {
		return "unknown", nil
	}
	magic := []byte{0xfd, '7', 'z', 'X', 'Z', 0}
	scan := k
	for tries := 0; tries < 32; tries++ {
		i := bytes.Index(scan, magic)
		if i < 0 || len(scan)-i < size {
			break
		}
		sum := sha256.Sum256(scan[i : i+size])
		if hex.EncodeToString(sum[:]) == hash {
			return "multiboot", nil
		}
		scan = scan[i+len(magic):]
	}
	// OpenATV can update chkroot independently of ODR. Also accept its
	// model-matched kernel with the release's explicit multiboot feature.
	props := image.features
	major, _, _ := strings.Cut(props["imageversion"], ".")
	v, _ := strconv.Atoi(major)
	if props["distro"] == "openatv" && v >= 8 && strings.EqualFold(props["chkrootmb"], "true") &&
		bytes.HasPrefix(k[banner[1]:], []byte("openATV@www.opena.tv)")) {
		return "multiboot", nil
	}
	return "unknown", nil
}

func imageProperties(data []byte) map[string]string {
	props := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = strings.Trim(strings.TrimSpace(v), "\"'")
		}
	}
	return props
}

func primaryPolicy(p Plan, l Layout) (bool, error) {
	status, err := sharedKernelStatus(p.Kernel, p.inspection)
	if err != nil {
		return false, err
	}
	if status == "unverified" {
		return false, errors.New("Cannot verify the receiver model of the slot 1 kernel")
	}
	classic := status != "multiboot"
	if classic && l.Multi {
		reason := "ODR could not verify this image as a source of the shared kernel and drivers."
		if status == "legacy" {
			reason = "DreamOS in slot 1 disables multiboot. Guest images depend on the kernel and drivers provided by slot 1."
		}
		if err := confirmSingleImage(reason); err != nil {
			return false, err
		}
	}
	return classic, nil
}

func installPrimary(p Plan, l Layout, base, next string, classic bool) error {
	previousDir, err := os.MkdirTemp(p.Directory, "previous-kernel-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(previousDir)
	previous := Plan{Directory: previousDir, Kernel: filepath.Join(previousDir, "kernel")}
	f, err := os.Create(previous.Kernel)
	if err != nil {
		return err
	}
	err = backupKernel(f)
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err = prepareClassicKernel(previous); err != nil {
		return err
	}
	write := func() error {
		job("working", "Writing and verifying the slot 1 kernel")
		if err := flashKernel(p); err != nil {
			job("working", "Restoring the previous kernel")
			if restore := flashKernel(previous); restore != nil {
				return fmt.Errorf("Kernel write failed (%v); restoring the previous kernel failed: %w", err, restore)
			}
			return fmt.Errorf("Previous kernel restored: %w", err)
		}
		return nil
	}
	if !classic {
		return write()
	}
	return installAtRoot(base, next, l.Multi, write)
}

// Publish the validated image at the partition root, where a DreamOS kernel
// looks for /sbin/init. Never format the partition or remove guest images.
func installAtRoot(base, next string, multi bool, write func() error) error {
	items, err := os.ReadDir(next)
	if err != nil {
		return err
	}
	var names []string
	for _, item := range items {
		n := item.Name()
		if n == "lost+found" {
			continue
		}
		if strings.HasPrefix(n, "linuxrootfs") || strings.HasPrefix(n, ".odr-") {
			return fmt.Errorf("Reserved image path %q", n)
		}
		if _, err := os.Lstat(filepath.Join(base, n)); !os.IsNotExist(err) {
			return fmt.Errorf("Cannot replace existing partition-root entry %q", n)
		}
		names = append(names, n)
	}
	commit := func() error {
		return moveForInstall(next, base, names, write)
	}
	if multi {
		err = bootMount(true, func(boot string) error {
			check, err := layoutAt(boot)
			if err != nil {
				return err
			}
			s, err := lookup(check, 1)
			if err != nil || !check.Multi || s.Device != rootDev || s.Folder != "linuxrootfs1" {
				return errors.New("The slot 1 configuration changed")
			}
			entries, err := os.ReadDir(boot)
			if err != nil {
				return err
			}
			var config []string
			for _, entry := range entries {
				if startFile.MatchString(entry.Name()) {
					if !entry.Type().IsRegular() {
						return errors.New("Unexpected STARTUP directory")
					}
					config = append(config, entry.Name())
				}
			}
			park, err := os.MkdirTemp(boot, ".odr-startup-")
			if err != nil {
				return err
			}
			if err = moveForInstall(boot, park, config, commit); err != nil {
				os.Remove(park) // Keep any files whose rollback failed.
				return err
			}
			return os.RemoveAll(park)
		})
	} else {
		err = commit()
	}
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(base, "linuxrootfs1"))
}

// Both directories are on the same filesystem. Undo completed renames in
// reverse order if a later rename or the verified kernel write fails.
func moveForInstall(from, to string, names []string, commit func() error) error {
	done := 0
	var err error
	for _, n := range names {
		if err = os.Rename(filepath.Join(from, n), filepath.Join(to, n)); err != nil {
			break
		}
		done++
	}
	if err == nil {
		syscall.Sync()
		err = commit()
	}
	if err != nil {
		for i := done - 1; i >= 0; i-- {
			n := names[i]
			err = errors.Join(err, os.Rename(filepath.Join(to, n), filepath.Join(from, n)))
		}
		syscall.Sync()
	}
	return err
}
