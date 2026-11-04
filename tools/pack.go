// Build the rescue initramfs while preserving original newc inode numbers,
// permissions, hard links and device numbers.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func read(name string) []byte { b, e := os.ReadFile(name); must(e); return b }
func gunzip(b []byte) ([]byte, error) {
	g, e := gzip.NewReader(bytes.NewReader(b))
	if e != nil {
		return nil, e
	}
	defer g.Close()
	g.Multistream(false)
	return io.ReadAll(io.LimitReader(g, 128<<20))
}
func aligned(n int) int { return (n + 3) &^ 3 }
func main() {
	model := os.Getenv("ODR_MODEL")
	if model == "" {
		model = "dm920"
	}
	bankedKernel := model == "dm7080" || model == "dm820"
	if len(os.Args) != 8 {
		panic("pack factory-rescue.bin server odr-hdmi drivers load-drivers.sh start-ssh.sh output.cpio")
	}
	img := read(os.Args[1])
	factoryHash := map[string]string{
		"dm520":  "0eb59f1b4cea694ad375f6d4dc573f6496960d1a8a24e8ffbddc2941e1fe5ecf",
		"dm820":  "df57263ae4a4ea004b5e3715d08bb64d414ab2c56fd6ae1414defd82a5388c69",
		"dm7080": "dd48aa167f75c870d1724ed388dda7e8f746f0789871e8419718edc0b159e812",
		"dm900":  "84db37c72ebe168f0a50769568f6363afe1adf08c12ffa6c21e8f399a593986d",
		"dm920":  "df63dedaccfc429ddf9789a0be2eb738f72200649638d985d4dc0174d72829b7",
	}
	if fmt.Sprintf("%x", sha256.Sum256(img)) != factoryHash[model] {
		panic("Wrong factory rescue base for " + model)
	}
	modules := "lib/modules/3.14-1.17-" + model
	server := "usr/sbin/odr-" + model
	offset := 0x4578
	machine := uint16(40)
	if bankedKernel || model == "dm520" {
		offset = 0
		modules = "lib/modules/3.4-4.0-" + model
		machine = 8
	}
	kernel, e := gunzip(img[offset:])
	must(e)
	var cpio []byte
	if machine == 8 {
		f, err := elf.NewFile(bytes.NewReader(kernel))
		must(err)
		section := f.Section(".init.data")
		if f.Machine != elf.EM_MIPS || f.Data != elf.ELFDATA2LSB || section == nil {
			panic("Unexpected MIPS rescue ELF")
		}
		data, err := section.Data()
		must(err)
		off := bytes.Index(data, []byte("070701"))
		if off < 0 || len(data)-off < 112 || string(data[off+110:off+112]) != ".\x00" {
			panic("Missing factory newc root")
		}
		cpio = data[off:]
	}
	for i := 0; cpio == nil && i < len(kernel); {
		off := bytes.Index(kernel[i:], []byte{31, 139, 8})
		if off < 0 {
			break
		}
		i += off
		b, e := gunzip(kernel[i:])
		if e == nil && bytes.HasPrefix(b, []byte("070701")) {
			cpio = b
			break
		}
		i++
	}
	var out bytes.Buffer
	var init []byte
	var sshService []byte
	found := false
	drop := regexp.MustCompile("^etc/rc[2-5]\\.d/S[0-9]+(lighttpd|autoflashd|dropbear)$|^etc/rc[0-6]\\.d/[SK][0-9]+recovery-ui$")
	kept := map[string]bool{}
	for i := 0; i+110 <= len(cpio); {
		if string(cpio[i:i+6]) != "070701" {
			panic("Invalid newc")
		}
		number := func(f int) int {
			v, e := strconv.ParseUint(string(cpio[i+6+f*8:i+14+f*8]), 16, 32)
			must(e)
			return int(v)
		}
		n, size := number(11), number(6)
		name := string(cpio[i+110 : i+110+n-1])
		start := aligned(i + 110 + n)
		end := aligned(start + size)
		if name == "TRAILER!!!" {
			found = true
			break
		}
		switch {
		case machine == 8 && name == "sbin/mkfs.xfs":
			// ODR only creates ext4 for internal image installations.
		case name == "etc/inittab":
			init = cpio[start : start+size]
		case name == "etc/xinetd.d/dropbear":
			sshService = cpio[start : start+size]
		case strings.HasPrefix(name, modules+"/") || name == modules:
		case strings.HasPrefix(name, "usr/lib/libnexus-ui") || name == "usr/bin/recovery-ui" || name == "etc/init.d/recovery-ui":
		case name == "etc/modules-load.d/00-bcmdriver.conf" || name == "etc/modules-load.d/01-dreambox-dvb-modules-stb-core.conf" || name == "etc/modules-load.d/02-dreambox-dvb-modules-lcd.conf":
		case name == "etc/issue" || name == "etc/issue.net" || name == "usr/sbin/autoflashd" || name == "etc/systemd/system/multi-user.target.wants/lighttpd.service" || drop.MatchString(name):
		default:
			out.Write(cpio[i:end])
			kept[name] = true
		}
		i = end
	}
	if !found || !bytes.Contains(init, []byte("id:5:initdefault:")) || len(sshService) == 0 {
		panic("Unexpected initramfs")
	}
	ino := 0x71000000
	rec := func(name string, mode int, b []byte) {
		ino++
		out.WriteString("070701")
		for _, v := range []int{ino, mode, 0, 0, 1, 0, len(b), 0, 0, 0, 0, len(name) + 1, 0} {
			fmt.Fprintf(&out, "%08x", v)
		}
		out.WriteString(name + "\x00")
		out.Write(make([]byte, aligned(out.Len())-out.Len()))
		out.Write(b)
		out.Write(make([]byte, aligned(out.Len())-out.Len()))
	}
	rec("etc/inittab", 0100644, []byte(strings.TrimRight(string(init), "\n")+"\nOD:5:respawn:/"+server+"\nSS:2345:once:/usr/sbin/odr-start-ssh\n"))
	rec("etc/dropbear/ssh.xinetd", 0100644, sshService)
	rec("usr/sbin/odr-start-ssh", 0100755, read(os.Args[6]))
	rec("etc/issue", 0100644, []byte("Open Dream Rescue 1.0 for "+strings.ToUpper(model)+"\n"))
	rec("etc/odr-"+model, 0100644, []byte("1.0\n"))
	addELF := func(name, path string) {
		elf := read(path)
		if len(elf) < 20 || !bytes.HasPrefix(elf, []byte{127, 69, 76, 70, 1, 1}) || binary.LittleEndian.Uint16(elf[18:]) != machine {
			panic("Wrong ELF architecture: " + name)
		}
		rec(name, 0100755, elf)
	}
	trust := read("/etc/ssl/certs/ca-certificates.crt")
	if !bytes.Contains(trust, []byte("-----BEGIN CERTIFICATE-----")) {
		panic("Missing build host CA bundle")
	}
	rec("etc/odr-firmware-ca.pem", 0100644, trust)
	addELF(server, os.Args[2])
	addELF("usr/sbin/odr-hdmi", os.Args[3])
	if bankedKernel {
		addELF("usr/sbin/odr-bank", filepath.Join(filepath.Dir(os.Args[2]), "odr-bank"))
	}
	root := os.Args[4]
	must(filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if name == "." || (kept[name] && d.IsDir()) {
			return nil
		}
		if kept[name] {
			return fmt.Errorf("Duplicate initramfs path %s", name)
		}
		if d.IsDir() {
			rec(name, 0040755, nil)
		} else if d.Type().IsRegular() {
			rec(name, 0100644, read(path))
		} else {
			return fmt.Errorf("Unexpected driver file type: %s", name)
		}
		return nil
	}))
	rec("usr/sbin/odr-drivers", 0100755, read(os.Args[5]))
	rec("etc/rcS.d/S06odr-drivers", 0120777, []byte("/usr/sbin/odr-drivers"))
	rec("TRAILER!!!", 0, nil)
	must(os.WriteFile(os.Args[7], out.Bytes(), 0644))
}
