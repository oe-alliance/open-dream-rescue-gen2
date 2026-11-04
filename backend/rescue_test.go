package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedKernelLayout(t *testing.T) {
	d := t.TempDir()
	for i, text := range map[string]string{"STARTUP": "kernel=" + p1 + " root=" + rootDev + " rootsubdir=linuxrootfs1", "STARTUP_1": "kernel=" + p1 + " root=" + rootDev + " rootsubdir=linuxrootfs1", "STARTUP_6": "kernel=" + p1 + " root=" + dataDev + " rootsubdir=linuxrootfs6"} {
		if err := os.WriteFile(filepath.Join(d, i), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	l, e := layoutAt(d)
	if e != nil || !l.Multi || l.Selected != 1 || len(l.Slots) != 2 || l.Slots[1].Device != dataDev {
		t.Fatal(l, e)
	}
	if e = selectSlot(d, l.Slots[1]); e != nil {
		t.Fatal(e)
	}
	l, e = layoutAt(d)
	if e != nil || l.Selected != 6 {
		t.Fatal(l, e)
	}
	if _, e = parseSlot(6, []byte("kernel="+p1+" root="+dataDev+" rootsubdir=../data")); e == nil {
		t.Fatal("accepted invalid folder")
	}
}
func TestSingleAndIncomplete(t *testing.T) {
	d := t.TempDir()
	l, e := layoutAt(d)
	if e != nil || l.Multi || len(l.Slots) != 1 {
		t.Fatal(l, e)
	}
	os.WriteFile(filepath.Join(d, "STARTUP"), nil, 0600)
	if _, e = layoutAt(d); e == nil {
		t.Fatal("ambiguous layout allowed")
	}
}

func TestDM9x0KernelVersionLabel(t *testing.T) {
	legacy := "kernel=/dev/3.14 root=" + dataDev + " rootsubdir=linuxrootfs6\n"
	if model != "dm900" && model != "dm920" {
		if _, err := parseSlot(6, []byte(legacy)); err == nil {
			t.Fatal("accepted DM9x0 kernel label on", model)
		}
		return
	}
	dir := t.TempDir()
	one := "kernel=" + p1 + " root=" + rootDev + " rootsubdir=linuxrootfs1\n"
	for name, contents := range map[string]string{"STARTUP": one, "STARTUP_1": one, "STARTUP_6": legacy} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	l, err := layoutAt(dir)
	if err != nil || !l.Multi || l.Selected != 1 || len(l.Slots) != 2 || l.Slots[1].Device != dataDev {
		t.Fatal(l, err)
	}
	if err = selectSlot(dir, l.Slots[1]); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filepath.Join(dir, "STARTUP"))
	if err != nil || string(current) != legacy {
		t.Fatal("STARTUP must retain the original slot contents", err)
	}
	l, err = layoutAt(dir)
	if err != nil || l.Selected != 6 {
		t.Fatal(l, err)
	}
	for _, invalid := range []string{
		strings.Replace(legacy, "/dev/3.14", "/dev/3.14.1", 1),
		strings.Replace(legacy, "/dev/3.14", "/dev/mmcblk0p3", 1),
		strings.Replace(legacy, dataDev, "/dev/sda1", 1),
		strings.Replace(legacy, "linuxrootfs6", "linuxrootfs1", 1),
		legacy + "kernel=" + p1,
	} {
		if _, err := parseSlot(6, []byte(invalid)); err == nil {
			t.Fatal("accepted unsupported STARTUP:", invalid)
		}
	}
}
func imageZIP(t *testing.T, d, model string) string {
	k := make([]byte, 256)
	binary.LittleEndian.PutUint32(k[36:], 0x016f2818)
	binary.LittleEndian.PutUint32(k[44:], uint32(len(k)))
	if model == "dm7080" || model == "dm820" {
		k = make([]byte, 8192)
		copy(k[100:], "Linux version 3.4-4.0-"+model+" (tester) ")
	}
	if model == "dm520" {
		k = make([]byte, 8192)
		copy(k, "\x7fELF\x01\x01\x01")
		binary.LittleEndian.PutUint16(k[16:], 2)
		binary.LittleEndian.PutUint16(k[18:], 8)
		copy(k[100:], "Linux version 3.4-4.0-dm520 (tester) ")
		var b bytes.Buffer
		g := gzip.NewWriter(&b)
		g.Write(k)
		g.Close()
		k = b.Bytes()
	}
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	for _, n := range []string{"sbin/init", "sbin/ldconfig"} {
		if e := tw.WriteHeader(&tar.Header{Name: n, Mode: 0755, Size: 1}); e != nil {
			t.Fatal(e)
		}
		tw.Write([]byte{'x'})
	}
	tw.Close()
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for n, b := range map[string][]byte{"kernel.bin": k, "rootfs.tar": tb.Bytes()} {
		w, e := zw.Create(model + "/" + n)
		if e != nil {
			t.Fatal(e)
		}
		w.Write(b)
	}
	zw.Close()
	name := filepath.Join(d, "test.zip")
	if e := os.WriteFile(name, zb.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	return name
}
func TestRejectDamagedBackupBeforeSelectingTarget(t *testing.T) {
	var data bytes.Buffer
	z := zip.NewWriter(&data)
	w, err := z.CreateHeader(&zip.FileHeader{Name: model + "/rootfs.tar.gz", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0x55}, 2048)
	copy(payload, []byte{0x1f, 0x8b, 8})
	if _, err = w.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	offset := bytes.Index(data.Bytes(), payload)
	if offset < 0 {
		t.Fatal("Missing stored ZIP payload")
	}
	data.Bytes()[offset+1024] ^= 1
	dir := t.TempDir()
	file := filepath.Join(dir, "backup.zip")
	if err = os.WriteFile(file, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = openImage(dir, file); err == nil || !strings.Contains(err.Error(), "CRC mismatch") {
		t.Fatalf("Damaged backup: %v", err)
	}
}

func TestPrepareModel(t *testing.T) {
	d := t.TempDir()
	p, e := prepare(d, imageZIP(t, d, model))
	if e != nil || p.Expanded != 2 {
		t.Fatal(p, e)
	}
	d = t.TempDir()
	other := "dm900"
	if model == other {
		other = "dm920"
	}
	if _, e = prepare(d, imageZIP(t, d, other)); e == nil {
		t.Fatal("accepted image for", other)
	}
}
func TestUnsafeNames(t *testing.T) {
	for _, n := range []string{"../root", "a/../a", "/dev/mmcblk0"} {
		if _, e := normal(n); e == nil {
			t.Fatal(n)
		}
	}
}

func TestSlotChecksTheStreamItInstalls(t *testing.T) {
	d := t.TempDir()
	file := imageZIP(t, d, model)
	p, err := openImage(d, file)
	if err != nil || p.Expanded != 0 {
		t.Fatal(p, err)
	}
	var decoded bytes.Buffer
	if err = p.checkRoot(&decoded, 1); err == nil {
		t.Fatal("accepted an image larger than the available destination")
	}
	decoded.Reset()
	if err = p.checkRoot(&decoded, 2); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&decoded)
	for _, name := range []string{"sbin/init", "sbin/ldconfig"} {
		h, err := tr.Next()
		if err != nil || h.Name != name {
			t.Fatal(h, err)
		}
		data, err := io.ReadAll(tr)
		if err != nil || string(data) != "x" {
			t.Fatal(data, err)
		}
	}
	if _, err = tr.Next(); err != io.EOF {
		t.Fatal(err)
	}
}
func TestRetainedSlotOneUsesSharedKernel(t *testing.T) {
	if nandImage {
		t.Skip("DM52x has no shared-kernel slots")
	}
	d := t.TempDir()
	if e := os.MkdirAll(filepath.Join(d, "linuxrootfs1", "sbin"), 0755); e != nil {
		t.Fatal(e)
	}
	for _, n := range []string{"init", "ldconfig"} {
		os.WriteFile(filepath.Join(d, "linuxrootfs1", "sbin", n), []byte("fixture"), 0755)
	}
	l := Layout{Slots: []Slot{{ID: 1, Device: rootDev}}}
	if e := classifySingle(&l, d, true); e != nil || l.Multi || l.Slots[0].Folder != "linuxrootfs1" {
		t.Fatal(l, e)
	}
	if e := os.Rename(filepath.Join(d, "linuxrootfs1", "sbin"), filepath.Join(d, "sbin")); e != nil {
		t.Fatal(e)
	}
	l = Layout{Slots: []Slot{{ID: 1, Device: rootDev}}}
	if e := classifySingle(&l, d, true); e != nil || l.Slots[0].Folder != "" {
		t.Fatal(l, e)
	}
}
func TestReleasedImages(t *testing.T) {
	for _, key := range []string{"ODR_TEST_ATV", "ODR_TEST_DREAMOS"} {
		name := os.Getenv(key)
		if name == "" {
			continue
		}
		t.Run(key, func(t *testing.T) {
			d := t.TempDir()
			p, e := prepare(d, name)
			if e != nil || p.Expanded < 100<<20 {
				t.Fatal(p, e)
			}
			t.Logf("verified %s, expanded %d", name, p.Expanded)
		})
	}
}
