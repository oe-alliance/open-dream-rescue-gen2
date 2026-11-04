package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DeviceGroup is shared by the web and HDMI views. All values are display
// strings. Collecting them does not enumerate or mount any image slots.
type DeviceGroup struct {
	Title  string
	Fields []DeviceField
}

type DeviceField struct{ Label, Value string }

func (g *DeviceGroup) add(label, value string) {
	if value != "" {
		g.Fields = append(g.Fields, DeviceField{label, value})
	}
}

func factoryFields(output string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || len(value) == 0 || len(value) > 64 {
			continue
		}
		switch key {
		case "CA_DATE":
			if day, err := time.Parse("20060102", value); err == nil {
				values[key] = day.Format("2006-01-02")
			}
		case "CA_SERIAL":
			if _, err := strconv.ParseUint(value, 10, 64); err == nil {
				values[key] = value
			}
		case "CA_ASERIAL":
			if strings.IndexFunc(value, func(c rune) bool {
				return !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '-')
			}) < 0 {
				values[key] = value
			}
		}
	}
	return values
}

var factory struct {
	sync.Mutex
	fields map[string]string
	retry  time.Time
}

func boxIdentity() map[string]string {
	factory.Lock()
	defer factory.Unlock()
	if len(factory.fields) > 0 || time.Now().Before(factory.retry) {
		return factory.fields
	}
	// This Broadcom rescue uses tpmd. Its factory helper also prints other
	// certificate fields; never evaluate, log, or expose its raw output.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "/usr/bin/tpm-ca").Output(); err == nil {
		factory.fields = factoryFields(string(output))
	}
	factory.retry = time.Now().Add(30 * time.Second)
	return factory.fields
}

func number(path string) uint64 {
	n, _ := strconv.ParseUint(readText(path), 10, 64)
	return n
}

func capacity(n uint64) string {
	switch {
	case n == 0:
		return ""
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(n)/(1<<30))
	case n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	default:
		return fmt.Sprintf("%.2f MiB", float64(n)/(1<<20))
	}
}

func memoryBanks(reg []byte, addresses, sizes uint32) uint64 {
	if addresses < 1 || addresses > 2 || sizes < 1 || sizes > 2 {
		return 0
	}
	width := int((addresses + sizes) * 4)
	if len(reg)%width != 0 {
		return 0
	}
	var total uint64
	for len(reg) != 0 {
		size := reg[addresses*4 : width]
		n := uint64(binary.BigEndian.Uint32(size))
		if sizes == 2 {
			n = binary.BigEndian.Uint64(size)
		}
		if n > math.MaxUint64-total {
			return 0
		}
		total += n
		reg = reg[width:]
	}
	return total
}

func installedRAM() uint64 {
	const dt = "/proc/device-tree/"
	cell := func(name string) uint32 {
		data, _ := os.ReadFile(dt + name)
		if len(data) == 4 {
			return binary.BigEndian.Uint32(data)
		}
		return 0
	}
	reg, _ := os.ReadFile(dt + "memory/reg")
	return memoryBanks(reg, cell("#address-cells"), cell("#size-cells"))
}

func deviceInfo() []DeviceGroup {
	identity := boxIdentity()
	receiver := DeviceGroup{Title: "Receiver"}
	receiver.add("Model", strings.ToUpper(readText("/proc/stb/info/model")))
	receiver.add("Model ID", readText("/proc/stb/info/mid"))
	receiver.add("Printed serial number", identity["CA_ASERIAL"])
	receiver.add("Serial number", identity["CA_SERIAL"])
	receiver.add("Production date", identity["CA_DATE"])
	receiver.add("Board", strings.TrimRight(readText("/proc/device-tree/model"), "\x00"))

	system := DeviceGroup{Title: "System"}
	system.add("Open Dream Rescue", readText("/etc/odr-"+model))
	system.add("Linux kernel", readText("/proc/sys/kernel/osrelease"))
	system.add("BOLT bootloader", strings.TrimRight(readText("/proc/device-tree/bolt/tag"), "\x00"))
	if chip := readText("/sys/devices/soc0/soc_id"); chip != "" {
		system.add("SoC", strings.TrimSpace("Broadcom BCM"+chip+" "+readText("/sys/devices/soc0/revision")))
	}
	cpus := 0
	var cpu string
	for _, line := range strings.Split(readText("/proc/cpuinfo"), "\n") {
		key, value, _ := strings.Cut(line, ":")
		switch strings.TrimSpace(key) {
		case "processor":
			cpus++
		case "model name":
			cpu = strings.TrimSpace(value)
		}
	}
	if cpus > 0 {
		system.add("CPU", fmt.Sprintf("%d cores · %s", cpus, cpu))
	}
	if fields := strings.Fields(readText("/proc/uptime")); len(fields) != 0 {
		if seconds, err := strconv.ParseFloat(fields[0], 64); err == nil {
			system.add("Uptime", (time.Duration(seconds) * time.Second).String())
		}
	}
	memory := DeviceGroup{Title: "Memory and storage"}
	memory.add("Installed RAM", capacity(installedRAM()))
	for _, line := range strings.Split(readText("/proc/meminfo"), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != "kB" {
			continue
		}
		n, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			memory.add("Linux RAM", capacity(n*1024))
		case "MemAvailable:":
			memory.add("Available RAM", capacity(n*1024))
		}
	}
	if nandImage {
		memory.add("Internal NAND", capacity(number("/sys/class/mtd/mtd1/size")))
	}
	memory.add("Internal eMMC", capacity(number("/sys/class/block/mmcblk0/size")*512))
	memory.add("eMMC device", readText("/sys/class/block/mmcblk0/device/name"))
	parts, _ := filepath.Glob("/sys/class/mtd/mtd[0-9]*")
	for _, part := range parts {
		switch readText(part + "/name") {
		case "flash0", "entire_device":
			memory.add("SPI flash", capacity(number(part+"/size")))
		case rescuePart:
			memory.add("Rescue partition", capacity(number(part+"/size")))
		}
	}

	network := DeviceGroup{Title: "Network"}
	if eth, err := net.InterfaceByName("eth0"); err == nil {
		network.add("MAC address", eth.HardwareAddr.String())
		addrs, _ := eth.Addrs()
		for _, addr := range addrs {
			if ip, _, err := net.ParseCIDR(addr.String()); err == nil && ip.To4() != nil {
				network.add("IPv4 address", ip.String())
			}
		}
		network.add("Ethernet", readText("/sys/class/net/eth0/operstate"))
		if speed := number("/sys/class/net/eth0/speed"); speed != 0 {
			link := fmt.Sprintf("%d Mbit/s", speed)
			if duplex := readText("/sys/class/net/eth0/duplex"); duplex == "full" || duplex == "half" {
				link += " · " + duplex + " duplex"
			}
			network.add("Link speed", link)
		}
	}
	interfaces, _ := filepath.Glob("/sys/class/net/*/wireless")
	wifi := []string{}
	for _, path := range interfaces {
		wifi = append(wifi, filepath.Base(filepath.Dir(path)))
	}
	if len(wifi) == 0 {
		network.add("Wi-Fi", "No wireless interface detected")
	} else {
		network.add("Wi-Fi", strings.Join(wifi, ", "))
	}
	return []DeviceGroup{receiver, system, memory, network}
}
