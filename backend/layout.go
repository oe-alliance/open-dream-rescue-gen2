package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const bootDev = "/dev/mmcblk0boot1"
const p1 = "/dev/mmcblk0p1"
const runDir = "/run/odr-" + model

type Slot struct {
	ID                    int
	Device, Folder, Label string
	Installed             bool
	Incomplete            bool `json:",omitempty"`
}
type Layout struct {
	Multi    bool
	Profile  string
	Selected int
	Slots    []Slot
}

var slotFile = regexp.MustCompile("^STARTUP_([1-9][0-9]?)$")
var startFile = regexp.MustCompile("^STARTUP($|_[A-Za-z0-9_-]+$)")

func readText(path string) string { b, _ := os.ReadFile(path); return strings.TrimSpace(string(b)) }
func parseSlot(id int, data []byte) (Slot, error) {
	s := Slot{ID: id}
	seen := map[string]bool{}
	for _, word := range strings.Fields(string(data)) {
		k, v, ok := strings.Cut(word, "=")
		if !ok {
			continue
		}
		if k == "kernel" || k == "root" || k == "rootsubdir" {
			if seen[k] {
				return s, errors.New("Duplicate STARTUP field")
			}
			seen[k] = true
		}
		switch k {
		case "kernel":
			if v != p1 {
				return s, fmt.Errorf("Unsupported kernel %s", v)
			}
		case "root":
			s.Device = v
		case "rootsubdir":
			s.Folder = v
		}
	}
	if (s.Device != rootDev && s.Device != dataDev) || s.Folder != "linuxrootfs"+strconv.Itoa(id) {
		return s, fmt.Errorf("Unsupported STARTUP_%d target; existing layout preserved", id)
	}
	return s, nil
}
func layoutAt(boot string) (Layout, error) {
	l := Layout{Slots: []Slot{}}
	entries, err := os.ReadDir(boot)
	if err != nil {
		return l, err
	}
	current := strings.Fields(readText(filepath.Join(boot, "STARTUP")))
	markers := false
	for _, f := range entries {
		if startFile.MatchString(f.Name()) {
			markers = true
		}
		m := slotFile.FindStringSubmatch(f.Name())
		if m == nil {
			continue
		}
		id, _ := strconv.Atoi(m[1])
		b, e := os.ReadFile(filepath.Join(boot, f.Name()))
		if e != nil {
			return l, e
		}
		s, e := parseSlot(id, b)
		if e != nil {
			return l, e
		}
		l.Slots = append(l.Slots, s)
		if strings.Join(current, " ") == strings.Join(strings.Fields(string(b)), " ") {
			l.Selected = id
		}
	}
	if len(l.Slots) == 0 {
		if markers {
			return l, errors.New("Incomplete STARTUP configuration")
		}
		return Layout{Slots: []Slot{{ID: 1, Device: rootDev}}}, nil
	}
	sort.Slice(l.Slots, func(i, j int) bool { return l.Slots[i].ID < l.Slots[j].ID })
	l.Multi = true
	l.Profile = "Multiboot"
	return l, nil
}
func command(name string, args ...string) error {
	c := exec.Command(name, args...)
	b, e := c.CombinedOutput()
	if e != nil {
		return fmt.Errorf("%s: %w: %.450s", name, e, b)
	}
	return nil
}
func mounted(dev, fs string, rw bool, fn func(string) error) (err error) {
	if dev != bootDev && dev != rootDev && dev != dataDev {
		return errors.New("Unrecognised mount target")
	}
	dir, e := os.MkdirTemp(runDir, "mnt-")
	if e != nil {
		return e
	}
	defer os.Remove(dir)
	flags := uintptr(syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC)
	opts := ""
	if !rw {
		flags |= syscall.MS_RDONLY
		if fs == "ext4" {
			// Linux 3.14 can clean orphan inodes even on a read-only ext4
			// mount. A read-only backing device also blocks these writes.
			loop, release, e := readOnlyLoop(dev)
			if e != nil {
				return e
			}
			defer release()
			dev = loop
			opts = "noload"
		}
	}
	if err = syscall.Mount(dev, dir, fs, flags, opts); err != nil {
		return err
	}
	defer func() {
		if e := syscall.Unmount(dir, 0); err == nil {
			err = e
		}
	}()
	return fn(dir)
}
func bootMount(rw bool, fn func(string) error) (err error) {
	flag := "/sys/block/mmcblk0boot1/force_ro"
	orig := readText(flag)
	if rw && orig == "1" {
		if err = os.WriteFile(flag, []byte("0"), 0600); err != nil {
			return err
		}
		defer func() {
			if e := os.WriteFile(flag, []byte("1"), 0600); err == nil {
				err = e
			}
		}()
	}
	return mounted(bootDev, "vfat", rw, fn)
}
func classifySingle(l *Layout, dir string, knownBoot bool) error {
	one := Slot{ID: 1, Device: rootDev}
	if knownBoot && incompleteSlot(dir, one) {
		st, err := os.Lstat(installMarker(dir, one))
		if err != nil || !st.Mode().IsRegular() || st.Size() > 16 {
			return errors.New("Invalid installation marker")
		}
		b, err := os.ReadFile(installMarker(dir, one))
		if err != nil || (len(b) != 0 && string(b) != "linuxrootfs1") {
			return errors.New("Invalid installation marker")
		}
		one.Folder, one.Incomplete = string(b), true
		l.Profile, l.Slots = "Single image", []Slot{one}
		return nil
	}
	if installed(dir) {
		if !knownBoot && !strings.Contains(strings.ToLower(readText(filepath.Join(dir, "etc/os-release"))+" "+readText(filepath.Join(dir, "etc/issue"))), "opendreambox") {
			return errors.New("Unrecognised boot area and root filesystem")
		}
		l.Profile = "Single image"
		return nil
	}
	s := Slot{ID: 1, Device: rootDev, Folder: "linuxrootfs1"}
	if knownBoot && slotReady(dir, s) {
		l.Profile = "Single image \u00b7 shared kernel"
		l.Slots = []Slot{s}
		return nil
	}
	if knownBoot {
		entries, e := os.ReadDir(dir)
		if e != nil {
			return e
		}
		for _, it := range entries {
			if strings.HasPrefix(it.Name(), "linuxrootfs") {
				return errors.New("Unrecognised image folder; internal storage preserved")
			}
		}
		l.Profile = "Single image"
		return nil
	}
	return errors.New("No recognised single-image layout; internal storage preserved")
}
func installed(dir string) bool {
	for _, p := range []string{"sbin/init", "sbin/ldconfig"} {
		if _, e := os.Lstat(filepath.Join(dir, p)); e != nil {
			return false
		}
	}
	return true
}
func slotRoot(base string, s Slot) string {
	if nandImage {
		return base
	}
	if s.Folder != "" {
		return filepath.Join(base, s.Folder)
	}
	if installed(base) {
		return base
	}
	return filepath.Join(base, "linuxrootfs1")
}
func installMarker(dir string, s Slot) string {
	return filepath.Join(dir, fmt.Sprintf(".odr-incomplete-%d", s.ID))
}
func incompleteSlot(dir string, s Slot) bool {
	_, err := os.Lstat(installMarker(dir, s))
	return !os.IsNotExist(err)
}
func slotReady(dir string, s Slot) bool {
	if nandImage && !nandBootable() {
		return false
	}
	if incompleteSlot(dir, s) {
		return false
	}
	dest := slotRoot(dir, s)
	st, e := os.Lstat(dest)
	return e == nil && st.IsDir() && installed(dest)
}
func lookup(l Layout, id int) (Slot, error) {
	for _, s := range l.Slots {
		if s.ID == id {
			return s, nil
		}
	}
	return Slot{}, errors.New("Unknown image slot")
}
func selectSlot(boot string, s Slot) error {
	path := filepath.Join(boot, "STARTUP_"+strconv.Itoa(s.ID))
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	checked, e := parseSlot(s.ID, b)
	if e != nil || checked.Device != s.Device {
		return errors.New("Slot configuration changed")
	}
	tmp := filepath.Join(boot, ".odr-next")
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		return e
	}
	return os.Rename(tmp, filepath.Join(boot, "STARTUP"))
}
func startSlot(l Layout, s Slot) error {
	if e := mounted(s.Device, rootFS, false, func(dir string) error {
		if incompleteSlot(dir, s) {
			return errors.New("Installation did not finish. Reinstall this image before starting it.")
		}
		if !slotReady(dir, s) {
			return errors.New("No bootable image in this slot")
		}
		return nil
	}); e != nil {
		return e
	}
	if l.Multi {
		return bootMount(true, func(dir string) error { return selectSlot(dir, s) })
	}
	return nil
}
func removeMulti(l Layout) error {
	one, e := lookup(l, 1)
	if e != nil || !l.Multi || one.Device != rootDev || one.Folder != "linuxrootfs1" {
		return errors.New("Slot 1 on internal rootfs is required")
	}
	e = mounted(rootDev, "ext4", false, func(dir string) error {
		if installed(dir) || !slotReady(dir, one) {
			return errors.New("Slot 1 must be the bootable fallback image")
		}
		return nil
	})
	if e != nil {
		return e
	}
	return bootMount(true, func(boot string) error {
		if e := selectSlot(boot, one); e != nil {
			return e
		}
		for _, s := range l.Slots {
			if s.ID == 1 {
				continue
			}
			e := mounted(s.Device, "ext4", true, func(dir string) error {
				name := filepath.Join(dir, s.Folder)
				st, er := os.Lstat(name)
				if os.IsNotExist(er) {
					return nil
				}
				if er != nil {
					return er
				}
				if !st.IsDir() {
					return errors.New("Invalid guest folder")
				}
				return os.RemoveAll(name)
			})
			if e != nil {
				return e
			}
		}
		entries, e := os.ReadDir(boot)
		if e != nil {
			return e
		}
		// Keep STARTUP_1 until last so an interrupted removal still selects the preserved image.
		for _, f := range entries {
			if startFile.MatchString(f.Name()) && f.Name() != "STARTUP" && f.Name() != "STARTUP_1" {
				if f.IsDir() {
					return errors.New("Unexpected STARTUP directory")
				}
				if e = os.Remove(filepath.Join(boot, f.Name())); e != nil {
					return e
				}
			}
		}
		if e = os.Remove(filepath.Join(boot, "STARTUP")); e != nil {
			return e
		}
		return os.Remove(filepath.Join(boot, "STARTUP_1"))
	})
}
