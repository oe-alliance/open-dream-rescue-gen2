package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// An update clicked during a read-back must run once the read completes.
// Another action during that wait must be rejected, not queued for later.
func TestUpdateDuringFirmwareRead(t *testing.T) {
	var g operationGate
	g.storage.RLock()
	done := make(chan bool, 1)
	go func() { done <- g.begin() }()
	deadline := time.Now().Add(3 * time.Second)
	for g.storage.TryRLock() {
		g.storage.RUnlock()
		if time.Now().After(deadline) {
			g.storage.RUnlock()
			t.Fatal("Update did not reserve the writer")
		}
		runtime.Gosched()
	}
	if g.begin() {
		t.Fatal("A second update was accepted")
	}
	g.storage.RUnlock()
	if !<-done {
		t.Fatal("Firmware read rejected the update")
	}
	if g.storage.TryRLock() {
		t.Fatal("Read allowed during update")
	}
	g.end()
	if !g.begin() {
		t.Fatal("Update gate remained busy")
	}
	g.end()
}

func TestRescueCatalogue(t *testing.T) {
	h := strings.Repeat("a", 64)
	size := fmt.Sprint(rescueLimit - 1)
	line := "rescue 1.0 " + size + " " + h + " https://example.invalid/" + model + ".bin"
	r, e := parseRescue([]byte("# DM920\n" + line + "\n"))
	if e != nil || r.SHA256 != h || r.Version != "1.0" {
		t.Fatal(r, e)
	}
	for _, bad := range []string{"", line + "\n" + line, strings.Replace(line, size, fmt.Sprint(rescueLimit+1), 1), strings.Replace(line, "https:", "http:", 1), strings.Replace(line, "rescue", "bootloader", 1), strings.Replace(line, h, "bad", 1)} {
		if _, e := parseRescue([]byte(bad)); e == nil {
			t.Fatal("Accepted invalid catalogue", bad)
		}
	}
}

func TestSPIReadbackAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		d := t.TempDir()
		dev, in := filepath.Join(d, "nor"), filepath.Join(d, "input")
		original := bytes.Repeat([]byte{0xee}, rescueLimit)
		image := []byte("new firmware")
		prefix := bytes.Repeat([]byte{0xab}, rescueOffset)
		if err := os.WriteFile(dev, append(prefix, original...), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(in, image, 0600); err != nil {
			t.Fatal(err)
		}
		r := RescueRelease{Bytes: int64(len(image)), SHA256: fmt.Sprintf("%x", sha256.Sum256(image))}
		writes := 0
		err := replaceRescue(d, in, dev, rescueOffset, r, func(path string) error {
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if fail && writes == 0 {
				b[0] ^= 1
			}
			f, e := os.OpenFile(dev, os.O_WRONLY, 0)
			if e != nil {
				return e
			}
			defer f.Close()
			writes++
			_, e = f.WriteAt(b, rescueOffset)
			return e
		})
		got, e := os.ReadFile(dev)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(got[:rescueOffset], prefix) {
			t.Fatal("Modified SPI prefix")
		}
		got = got[rescueOffset:]
		if fail {
			if err == nil || !strings.Contains(err.Error(), "restored and verified") || writes != 2 || !bytes.Equal(original, got) {
				t.Fatal("rollback failed", err, writes)
			}
		} else if err != nil || writes != 1 || !bytes.Equal(got[:len(image)], image) || !bytes.Equal(got[len(image):], original[len(image):]) {
			t.Fatal("write/verify failed", err)
		}
	}
}

func TestRescueBuildIdentity(t *testing.T) {
	for _, key := range []string{"ODR_TEST_RESCUE", "ODR_TEST_NON_ODR"} {
		path := os.Getenv(key)
		if path == "" {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		r := RescueRelease{Bytes: int64(len(b)), SHA256: fmt.Sprintf("%x", sha256.Sum256(b))}
		err = checkRescueImage(path, r)
		if (key == "ODR_TEST_RESCUE") != (err == nil) {
			t.Fatalf("%s: %v", key, err)
		}
		r.SHA256 = strings.Repeat("0", 64)
		if checkRescueImage(path, r) == nil {
			t.Fatal("Accepted incorrect digest")
		}
	}
}
