package main

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleasedSharedKernel(t *testing.T) {
	kernel := os.Getenv("ODR_TEST_SHARED_KERNEL")
	if kernel == "" {
		t.Skip("supply an original model-specific OpenATV kernel")
	}
	var image slotInspection
	release, chip := "3.14-1.17-"+model, "bcm7439.ko"
	if bankedKernel {
		release, chip = "3.4-4.0-"+model, "bcm7435.ko"
	}
	check := func(want string) {
		t.Helper()
		got, err := sharedKernelStatus(kernel, &image)
		if err != nil || got != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	check("unknown")
	if err := image.observe(strings.NewReader("x"), &tar.Header{Typeflag: tar.TypeReg, Size: 1}, "lib/modules/"+release+"/extra/"+chip); err != nil {
		t.Fatal(err)
	}
	check("multiboot") // no distro metadata: must recognise the embedded chkroot
	id := "ID=\"opendreambox\"\n"
	if err := image.observe(strings.NewReader(id), &tar.Header{Typeflag: tar.TypeReg, Size: int64(len(id))}, "etc/os-release"); err != nil {
		t.Fatal(err)
	}
	check("legacy") // a guest backup can contain the known shared kernel
}

func TestSlotOneRenameRollback(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	for _, name := range []string{"STARTUP", "STARTUP_1"} {
		if err := os.WriteFile(filepath.Join(from, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fault := errors.New("kernel read-back failed")
	err := moveForInstall(from, to, []string{"STARTUP", "STARTUP_1"}, func() error { return fault })
	if !errors.Is(err, fault) {
		t.Fatal(err)
	}
	for _, name := range []string{"STARTUP", "STARTUP_1"} {
		b, err := os.ReadFile(filepath.Join(from, name))
		if err != nil || string(b) != name {
			t.Fatal(name, err)
		}
	}
}

func TestClassicRootPreservesOtherDirectories(t *testing.T) {
	base := t.TempDir()
	for _, path := range []string{"linuxrootfs1/old", "linuxrootfs2/guest", "photos", "new/sbin"} {
		if err := os.MkdirAll(filepath.Join(base, path), 0755); err != nil {
			t.Fatal(err)
		}
	}
	next := filepath.Join(base, "new")
	fault := errors.New("verification failed")
	if err := installAtRoot(base, next, false, func() error { return fault }); !errors.Is(err, fault) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "linuxrootfs1/old")); err != nil {
		t.Fatal(err)
	}
	if err := installAtRoot(base, next, false, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"sbin", "linuxrootfs2/guest", "photos"} {
		if _, err := os.Stat(filepath.Join(base, path)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "linuxrootfs1")); !os.IsNotExist(err) {
		t.Fatal("Old slot 1 still present", err)
	}
}
