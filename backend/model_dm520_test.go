//go:build dm520

package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReleaseConsumedNANDPages(t *testing.T) {
	payload := make([]byte, 3<<20+31)
	rand.New(rand.NewSource(520)).Read(payload)
	for _, format := range []string{"raw", "stored-zip", "deflated-zip"} {
		t.Run(format, func(t *testing.T) {
			f, err := os.CreateTemp("/dev/shm", "odr-nand-read-")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			defer os.Remove(f.Name())
			var output io.Writer = f
			var z *zip.Writer
			if format != "raw" {
				z = zip.NewWriter(f)
				method := uint16(zip.Store)
				if format == "deflated-zip" {
					method = zip.Deflate
				}
				output, err = z.CreateHeader(&zip.FileHeader{Name: model + "/rootfs.tar.gz", Method: method})
				if err != nil {
					t.Fatal(err)
				}
			}
			g := gzip.NewWriter(output)
			if _, err = g.Write(payload); err != nil {
				t.Fatal(err)
			}
			if err = g.Close(); err != nil {
				t.Fatal(err)
			}
			if z != nil {
				if err = z.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			in := &nandTarInput{File: f, next: -1}
			hash := sha256.New()
			err = readTarAt(in, before.Size(), func(offset int64) { in.next = offset }, func(r io.Reader) error {
				_, e := io.Copy(hash, r)
				return e
			})
			want := sha256.Sum256(payload)
			if err != nil || !bytes.Equal(want[:], hash.Sum(nil)) {
				t.Fatalf("Read changed the payload: %v", err)
			}
			after, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if in.released < 3<<20 || after.Size() != before.Size() || after.Sys().(*syscall.Stat_t).Blocks >= before.Sys().(*syscall.Stat_t).Blocks {
				t.Fatal("Read did not free the consumed tmpfs pages")
			}
			if _, err = in.ReadAt(make([]byte, 1), 0); err == nil {
				t.Fatal("Read from consumed input succeeded")
			}
		})
	}
}

func TestDM52xReleasePackages(t *testing.T) {
	for _, format := range []string{"UBI", "WEB", "TAR"} {
		t.Run(format, func(t *testing.T) {
			file := os.Getenv("ODR_TEST_DM520_" + format)
			if file == "" {
				t.Skip("Supply a DM520 release package")
			}
			p, err := openImage(t.TempDir(), file)
			if err != nil {
				t.Fatal(err)
			}
			if format == "UBI" {
				if !p.UBI || p.RootKernel {
					t.Fatal("invalid flash ZIP plan")
				}
				err = withUBI(p, func(r io.Reader, size int64) error {
					n, e := io.Copy(io.Discard, r)
					if n != size {
						t.Fatalf("streamed %d of %d UBI bytes", n, size)
					}
					return e
				})
			} else {
				if p.UBI || !p.RootKernel {
					t.Fatal("invalid rootfs plan")
				}
				if err = locateNANDKernel(p); err != nil {
					t.Fatal(err)
				}
				checked, err := os.ReadFile(p.Kernel)
				if err != nil || !validImageKernel(checked) {
					t.Fatal("missing DM52x kernel before erase", err)
				}
				if err = p.checkRoot(nil, maxRoot); err != nil {
					t.Fatal(err)
				}
				extracted, err := os.ReadFile(p.Kernel)
				if err != nil || !bytes.Equal(checked, extracted) {
					t.Fatal("kernel changed during extraction", err)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDM52xRejectsInvalidUBIBeforeErasure(t *testing.T) {
	d := t.TempDir()
	f, e := os.Create(filepath.Join(d, "bad.zip"))
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	w, e := z.Create("dm520/rootfs.bin")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.Write(make([]byte, 3*nandBlock)); e != nil {
		t.Fatal(e)
	}
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	p := Plan{Tar: f.Name(), UBI: true}
	if withUBI(p, func(io.Reader, int64) error { t.Fatal("invoked NAND writer"); return nil }) == nil {
		t.Fatal("invalid UBI accepted")
	}
}
