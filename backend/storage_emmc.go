//go:build !dm520

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func nandBootable() bool      { return false }
func prepareWorkspace() error { return nil }

func layout() (l Layout, err error) {
	var entered bool
	err = bootMount(false, func(dir string) error { entered = true; var e error; l, e = layoutAt(dir); return e })
	blank := false
	if err != nil && !entered && errors.Is(err, syscall.EINVAL) {
		b, e := os.ReadFile(bootDev)
		if e != nil {
			return l, e
		}
		if len(b) != 4<<20 && !(bankedKernel && len(b) == 2<<20) {
			return l, errors.New("Unexpected boot area size")
		}
		blank = len(bytes.Trim(b, "\x00")) == 0 || len(bytes.Trim(b, "\xff")) == 0
		l = Layout{Slots: []Slot{{ID: 1, Device: rootDev}}}
	} else if err != nil || l.Multi {
		return
	}
	err = mounted(rootDev, "ext4", false, func(dir string) error {
		return classifySingle(&l, dir, entered || blank)
	})
	return
}
func install(p Plan, l Layout, s Slot) error {
	job("working", "Checking the destination")
	if err := idleDevice(s.Device); err != nil {
		return err
	}
	single := !l.Multi && s.Folder == ""
	if single && (s.ID != 1 || s.Device != rootDev) ||
		!single && (s.Folder != fmt.Sprintf("linuxrootfs%d", s.ID) || s.ID < 1 || (s.ID == 1 && s.Device != rootDev)) {
		return errors.New("Unrecognised image destination")
	}

	capacity := int64(number("/sys/class/block/"+filepath.Base(s.Device)+"/size")) * 512
	if capacity <= 0 {
		return errors.New("Unknown destination partition capacity")
	}
	if s.ID != 1 {
		return mounted(s.Device, "ext4", true, func(base string) error { return installGuest(p, base, s) })
	}

	// Slot 1 supplies the kernel. Check it and resolve the multiboot decision
	// before erasing the installed image. Only this path needs a replay.
	spool := optionalCache{discarded: true}
	free, err := available(p.Directory)
	if err != nil {
		return err
	}
	if free > 64<<20 {
		cache, err := os.CreateTemp(p.Directory, "decoded-")
		if err == nil {
			defer cache.Close()
			if err = os.Remove(cache.Name()); err != nil {
				return err
			}
			spool = optionalCache{file: cache, left: free - (64 << 20)}
		}
	}
	p.inspection = &slotInspection{}
	job("working", "Checking the slot 1 image and kernel before erasing")
	if err := p.checkRoot(&spool, maxRoot); err != nil {
		return err
	}
	classic := single
	if !single {
		classic, err = primaryPolicy(p, l)
		if err != nil {
			return err
		}
	}
	if err := prepareClassicKernel(p); err != nil {
		return err
	}
	extract := func(dir string) error {
		room, err := available(dir)
		if err != nil {
			return err
		}
		if room < p.Expanded {
			return fmt.Errorf("The cleared destination has %d MiB available; the image needs at least %d MiB", room>>20, (p.Expanded+(1<<20)-1)>>20)
		}
		job("working", "Extracting the image directly to the cleared destination")
		err = extractTo(dir, func(w io.Writer) error {
			if spool.file != nil && !spool.discarded {
				if _, err := spool.file.Seek(0, 0); err != nil {
					return err
				}
				_, err := io.Copy(w, spool.file)
				return err
			}
			// The archive in private RAM was already checked. Do not unpack
			// the kernel or validate a second target tree during extraction.
			return withTar(p.Tar, func(r io.Reader) error { _, err := io.CopyN(w, r, spool.seen); return err })
		})
		if err != nil {
			return err
		}
		if !installed(dir) {
			return errors.New("The extracted image is incomplete")
		}
		return nil
	}
	if p.Expanded > capacity {
		return errors.New("The image exceeds the destination partition capacity")
	}
	if single {
		erasingDestination("Formatting the internal root filesystem")
		if err := command("/sbin/mkfs.ext4", "-F", "-L", rootLabel, rootDev); err != nil {
			return err
		}
		err = mounted(rootDev, "ext4", true, func(base string) error {
			if err := markIncomplete(base, s); err != nil {
				return err
			}
			// mkfs.ext4 creates lost+found. The extractor needs an empty root.
			if err := os.Remove(filepath.Join(base, "lost+found")); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := extract(base); err != nil {
				return err
			}
			syscall.Sync()
			job("working", "Writing and verifying the internal image kernel")
			if err := flashKernel(p); err != nil {
				return err
			}
			return os.Remove(installMarker(base, s))
		})
		if err != nil {
			return fmt.Errorf("The internal image was cleared. Install it again before booting: %w", err)
		}
		return nil
	}
	return mounted(s.Device, "ext4", true, func(base string) error {
		target := filepath.Join(base, s.Folder)
		if classic {
			// A DreamOS kernel needs the root at partition level. Reject
			// collisions with unrelated files before clearing the old slot.
			for name := range p.inspection.top {
				if name == "lost+found" {
					continue
				}
				if strings.HasPrefix(name, "linuxrootfs") || strings.HasPrefix(name, ".odr-") {
					return fmt.Errorf("Reserved root entry %q", name)
				}
				if _, err := os.Lstat(filepath.Join(base, name)); !os.IsNotExist(err) {
					return fmt.Errorf("Cannot replace existing partition-root entry %q", name)
				}
			}
		}
		if err := clearSlot(base, s); err != nil {
			return err
		}
		if err := extract(target); err != nil {
			return errors.Join(fmt.Errorf("Slot %d was cleared. Install the image again before booting: %w", s.ID, err), os.RemoveAll(target))
		}
		if err := installPrimary(p, l, base, target, classic); err != nil {
			return fmt.Errorf("Slot 1 was replaced but installation did not finish: %w", err)
		}
		return os.Remove(installMarker(base, s))
	})
}
