package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var settingsPaths = []string{
	"etc/enigma2", "etc/tuxbox", "etc/network", "etc/hostname", "etc/hosts",
	"etc/resolv.conf", "etc/exports", "etc/fstab", "etc/videomode", "etc/wpa_supplicant.conf",
	"etc/dropbear", "etc/ssh", "etc/passwd", "etc/group", "etc/shadow", "etc/gshadow",
	"etc/machine-id", "etc/recovery", "etc/softcam", "root/.ssh",
	"var/lib/connman", "var/lib/NetworkManager", "usr/share/enigma2/picon",
}
var unsafeLabel = regexp.MustCompile("[^A-Za-z0-9._-]+")

func settingsEntry(name string, directory bool) bool {
	for _, p := range settingsPaths {
		if name == p || strings.HasPrefix(name, p+"/") || directory && strings.HasPrefix(p, name+"/") {
			return true
		}
	}
	return false
}

// Mount points belong in an installable image, their transient contents do not.
func emptyBackupDir(name string) bool {
	switch name {
	case "dev", "proc", "sys", "run", "tmp", "mnt", "media", "lost+found":
		return true
	}
	return false
}

func backupXattrs(path string, h *tar.Header) error {
	n, err := syscall.Listxattr(path, nil)
	if errors.Is(err, syscall.ENOTSUP) {
		return nil
	}
	if err != nil || n == 0 {
		return err
	}
	names := make([]byte, n)
	if _, err = syscall.Listxattr(path, names); err != nil {
		return err
	}
	h.Xattrs = make(map[string]string)
	for _, name := range strings.Split(strings.TrimSuffix(string(names), "\x00"), "\x00") {
		n, err = syscall.Getxattr(path, name, nil)
		if err != nil {
			return err
		}
		value := make([]byte, n)
		if _, err = syscall.Getxattr(path, name, value); err != nil {
			return err
		}
		h.Xattrs[name] = string(value)
	}
	return nil
}

func packFilesystem(w io.Writer, root string, settings bool) error {
	g := gzip.NewWriter(w)
	t := tar.NewWriter(g)
	links := make(map[uint64]string)
	var contents rootArchive
	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if (settings && !settingsEntry(name, d.IsDir())) || strings.HasPrefix(name, ".odr-") || strings.HasPrefix(name, "linuxrootfs") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if _, err := normal(name); err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSocket != 0 {
			return nil
		}
		var target string
		if st.Mode()&os.ModeSymlink != 0 {
			target, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		h, err := tar.FileInfoHeader(st, target)
		if err != nil {
			return err
		}
		h.Name = name
		stat := st.Sys().(*syscall.Stat_t)
		if st.Mode().IsRegular() && stat.Nlink > 1 {
			if src, ok := links[stat.Ino]; ok {
				h.Typeflag, h.Linkname, h.Size = tar.TypeLink, src, 0
			} else {
				links[stat.Ino] = name
			}
		}
		if h.Typeflag != tar.TypeSymlink && h.Typeflag != tar.TypeLink {
			if err := backupXattrs(path, h); err != nil {
				return err
			}
		}
		if !settings {
			if err := contents.entry(h, name, maxRoot); err != nil {
				return err
			}
		}
		if err := t.WriteHeader(h); err != nil {
			return err
		}
		if !d.IsDir() {
			files++
		}
		if h.Typeflag == tar.TypeReg && h.Size > 0 {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.CopyN(t, f, h.Size)
			f.Close()
			return err
		}
		if d.IsDir() && emptyBackupDir(name) {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return err
	}
	if files == 0 {
		return errors.New("No files found for this backup")
	}
	if !settings && (!contents.init || !contents.ldconfig) {
		return errors.New("Missing bootable root filesystem in backup")
	}
	if err := t.Close(); err != nil {
		return err
	}
	return g.Close()
}

func packImage(w io.Writer, root string) error {
	z := zip.NewWriter(w)
	member := func(name string) (io.Writer, error) {
		return z.CreateHeader(&zip.FileHeader{Name: model + "/" + name, Method: zip.Store})
	}
	k, err := member("kernel.bin")
	if err != nil {
		return err
	}
	job("working", "Copying the internal image kernel")
	if err = backupKernel(k); err != nil {
		return err
	}
	r, err := member("rootfs.tar.gz")
	if err != nil {
		return err
	}
	job("working", "Compressing the selected root filesystem")
	if err = packFilesystem(r, root, false); err != nil {
		return err
	}
	return z.Close()
}

type backupWriter struct {
	file *os.File
	left int64
}

func (w *backupWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.left {
		return 0, errors.New("Insufficient rescue RAM for this backup")
	}
	n, err := w.file.Write(p)
	w.left -= int64(n)
	return n, err
}

func backupFilename(label string, id int, kind, date string) string {
	ext := ".zip"
	if kind == "settings" {
		ext = ".tar.gz"
	}
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		day = time.Now()
	}
	label = strings.Trim(unsafeLabel.ReplaceAllString(strings.ToLower(label), "-"), "-._")
	if label != "" {
		label += "-"
	}
	return fmt.Sprintf(model+"-%sslot%d-%s-%s%s", label, id, kind, day.Format("2006-01-02"), ext)
}

func withBackupSource(id int, fn func(string, string) error) error {
	l, err := layout()
	if err != nil {
		return err
	}
	s, err := lookup(l, id)
	if err != nil {
		return err
	}
	return mounted(s.Device, rootFS, false, func(base string) error {
		if !slotReady(base, s) {
			return errors.New("The selected image is empty or unreadable")
		}
		root := slotRoot(base, s)
		label := imageLabel(root)
		jobMu.Lock()
		busyJob.Image = label
		jobMu.Unlock()
		return fn(root, label)
	})
}

func writeBackup(w io.Writer, root, kind string) error {
	if kind == "image" {
		return packImage(w, root)
	}
	job("working", "Archiving receiver, channel and network settings")
	return packFilesystem(w, root, true)
}

func backupSlot(id int, kind, date string) error {
	dir, err := os.MkdirTemp(runDir, "backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	output := filepath.Join(dir, "archive")
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	free, err := available(dir)
	if err != nil {
		return err
	}
	writer := &backupWriter{file: file, left: min(free-(64<<20), maxUpload)}
	var filename string
	err = withBackupSource(id, func(root, label string) error {
		filename = backupFilename(label, id, kind, date)
		return writeBackup(writer, root, kind)
	})
	if err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(output, filepath.Join(runDir, "backup")); err != nil {
		return err
	}
	jobMu.Lock()
	busyJob.BackupID, busyJob.Filename = filepath.Base(dir), filename
	jobMu.Unlock()
	job("done", "Backup ready. Download it before starting another operation or restarting the receiver.")
	return nil
}

// On DM52x the compressed image can fill most of RAM. The browser receives
// the ZIP as it is written, with no full-size output file on the receiver.
func streamBackup(w http.ResponseWriter, r *http.Request, id string) {
	if !op.begin() {
		http.Error(w, "Operation in progress", http.StatusConflict)
		return
	}
	defer op.end()
	jobMu.Lock()
	j := busyJob
	if j.State != "backup-download" || j.BackupID != id {
		jobMu.Unlock()
		http.NotFound(w, r)
		return
	}
	busyJob.BackupID = ""
	jobMu.Unlock()
	job("working", "Preparing backup download")
	started := false
	err := withBackupSource(j.Slot, func(root, label string) error {
		name := backupFilename(label, j.Slot, j.streamKind, j.streamDate)
		jobMu.Lock()
		busyJob.Filename = name
		jobMu.Unlock()
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		started = true
		if err := http.NewResponseController(w).Flush(); err != nil {
			return err
		}
		return writeBackup(w, root, j.streamKind)
	})
	if err != nil {
		job("error", "Backup download interrupted. Discard the incomplete file and create a new backup. "+err.Error())
		if started {
			panic(http.ErrAbortHandler)
		} // Never complete a truncated chunked download.
		failure(w, err)
		return
	}
	job("done", "Backup sent. Keep the downloaded file in a safe place.")
}

func serveBackup(w http.ResponseWriter, r *http.Request) {
	jobMu.Lock()
	j := busyJob
	if nandImage && j.State == "backup-download" && j.BackupID != "" && j.BackupID == r.URL.Query().Get("id") {
		jobMu.Unlock()
		streamBackup(w, r, j.BackupID)
		return
	}
	if j.Action != "backup" || j.State != "done" || j.BackupID == "" || r.URL.Query().Get("id") != j.BackupID {
		jobMu.Unlock()
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(runDir, "backup"))
	jobMu.Unlock()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": j.Filename}))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, j.Filename, time.Time{}, f)
}
