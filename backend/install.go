package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func idleDevice(dev string) error {
	if dev != p1 && dev != rootDev && dev != dataDev && dev != bootDev {
		return errors.New("Unsupported internal device")
	}
	id := readText("/sys/class/block/" + filepath.Base(dev) + "/dev")
	if id == "" {
		return errors.New("Missing internal device")
	}
	mounts, e := os.ReadFile("/proc/self/mountinfo")
	if e != nil {
		return e
	}
	for _, r := range strings.Split(string(mounts), "\n") {
		f := strings.Fields(r)
		if len(f) > 2 && f[2] == id {
			return fmt.Errorf("%s is already mounted", dev)
		}
	}
	return nil
}
func available(path string) (int64, error) {
	var s syscall.Statfs_t
	e := syscall.Statfs(path, &s)
	return int64(s.Bavail) * int64(s.Bsize), e
}

func erasingDestination(message string) {
	jobMu.Lock()
	busyJob.DestinationAtRisk = true
	jobMu.Unlock()
	job("working", message)
}

func markIncomplete(base string, s Slot) error {
	// O_EXCL avoids following existing links. A marker from a failed
	// attempt must stay in place throughout a retry.
	f, err := os.OpenFile(installMarker(base, s), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		st, e := os.Lstat(installMarker(base, s))
		if e == nil && st.Mode().IsRegular() {
			return nil
		}
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString(s.Folder)
	err = errors.Join(err, f.Close())
	syscall.Sync()
	return err
}
func clearSlot(base string, s Slot) error {
	target := filepath.Join(base, s.Folder)
	st, err := os.Lstat(target)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if st != nil && !st.IsDir() {
		return errors.New("Invalid slot directory")
	}
	if err := markIncomplete(base, s); err != nil {
		return err
	}
	erasingDestination(fmt.Sprintf("Clearing slot %d", s.ID))
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("Could not finish clearing slot %d: %w", s.ID, err)
	}
	return os.Mkdir(target, 0755)
}

func installGuest(p Plan, base string, s Slot) error {
	target := filepath.Join(base, s.Folder)
	cleared := false
	err := withTar(p.Tar, func(src io.Reader) error {
		r := bufio.NewReader(src)
		// Reject an empty or truncated first header without restarting the decoder.
		head, err := r.Peek(512)
		if err != nil {
			return err
		}
		if len(bytes.Trim(head, "\x00")) == 0 {
			return errors.New("Empty root filesystem archive")
		}
		if err := clearSlot(base, s); err != nil {
			return err
		}
		cleared = true
		room, err := available(target)
		if err != nil {
			return err
		}
		job("working", "Extracting the image directly to the cleared destination")
		return extractTo(target, func(w io.Writer) error { return p.checkTar(r, w, room) })
	})
	if err == nil && !installed(target) {
		err = errors.New("The extracted image is incomplete")
	}
	if err != nil {
		if cleared {
			return errors.Join(fmt.Errorf("Slot %d was cleared. Install the image again before booting: %w", s.ID, err), os.RemoveAll(target))
		}
		return err
	}
	job("working", "Slot installed with the existing multiboot kernel")
	return os.Remove(installMarker(base, s))
}

// If the optional decoded tar outgrows the RAM budget, keep
// validating the stream and replay the source after the user has decided.
type optionalCache struct {
	file      *os.File
	left      int64
	seen      int64
	discarded bool
}

func (c *optionalCache) Write(b []byte) (int, error) {
	c.seen += int64(len(b))
	if !c.discarded {
		if int64(len(b)) <= c.left {
			if n, err := c.file.Write(b); err == nil && n == len(b) {
				c.left -= int64(n)
				return n, nil
			}
		}
		c.discarded = true
		if err := c.file.Truncate(0); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}
