package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type displayPage struct {
	title string
	rows  []DeviceField
}

func devicePages(groups []DeviceGroup) []displayPage {
	var pages []displayPage
	for _, group := range groups {
		for i := 0; i < len(group.Fields); i += 4 {
			pages = append(pages, displayPage{group.Title, group.Fields[i:min(i+4, len(group.Fields))]})
		}
	}
	return pages
}

type displayMenu struct {
	layout                         Layout
	selected, page                 int
	pages                          []displayPage
	view, notice, layoutError, url string
}

func (m *displayMenu) inventory() bool {
	if !op.storage.TryRLock() {
		return false
	}
	defer op.storage.RUnlock()
	id := m.slot().ID
	l, warning, err := imageInventory()
	m.layout, m.selected, m.layoutError = l, 0, warning
	if err != nil {
		m.layout = Layout{}
		m.layoutError = err.Error()
		return true
	}
	if id == 0 {
		id = l.Selected
	}
	for i, s := range l.Slots {
		if s.ID == id {
			m.selected = i
			break
		}
	}
	return true
}

func (m *displayMenu) slot() Slot {
	if m.selected < len(m.layout.Slots) {
		return m.layout.Slots[m.selected]
	}
	return Slot{}
}

func (m *displayMenu) slotName(i int) string {
	if i < 0 || i >= len(m.layout.Slots) {
		return ""
	}
	s := m.layout.Slots[i]
	name := "Internal image"
	if m.layout.Multi {
		name = fmt.Sprintf("Slot %d", s.ID)
	}
	if s.Label != "" {
		name += " · " + s.Label
	}
	return name
}

func (m *displayMenu) frame() string {
	// Ordered wire format shared with hdmi/screen.h. No untrusted text can
	// create another field or frame.
	f := [12]string{}
	profile := m.layout.Profile
	if profile != "" {
		profile = "  |  " + profile
	}
	f[1] = "Dreambox " + modelName + "  |  ODR 1.0" + profile
	if nandImage {
		f[1] = "Dreambox " + strings.ToUpper(readText("/proc/stb/info/model")) + "  |  ODR 1.0" + profile
	}
	f[9] = m.url
	if f[9] == "" {
		f[9] = "Waiting for a network address ..."
	}
	f[10] = "Upload or download an image in your browser."
	if m.layout.Multi {
		f[10] = "For reliable multiboot, install OpenATV 8 or newer in slot 1."
	}
	f[11] = "UP / DOWN or DIGITS: select   |   OK: start image   |   INFO: device information"
	s := m.slot()
	switch m.view {
	case "device":
		f[0] = "device"
		if len(m.pages) > 0 {
			p := m.pages[m.page]
			f[2] = fmt.Sprintf("Device information   /   %s   (%d of %d)", p.title, m.page+1, len(m.pages))
			for i, field := range p.rows {
				f[3+i] = field.Label + "   " + field.Value
			}
		}
		f[11] = "ARROWS: previous / next page   |   INFO / BACK: slot list"
	case "confirm", "notice":
		f[0], f[2] = "message", "Start image"
		if m.view == "confirm" {
			f[5], f[6] = "Start "+m.slotName(m.selected)+"?", "The Dreambox will restart."
			f[11] = "OK: start now   |   BACK: cancel"
		} else {
			f[5], f[6], f[11] = m.notice, "", "BACK: return to slot list"
		}
	default:
		f[0], f[2] = "slots", "Select an image"
		if s.ID == 0 {
			f[5], f[6] = "No image slots available", m.layoutError
		} else {
			for i, field := range []int{3, 4, 5, 7, 8} {
				f[field] = m.slotName(m.selected + i - 2)
			}
			f[6] = "Empty or unreadable"
			if s.Installed {
				f[6] = "Installed"
			}
			if m.layout.Selected == s.ID {
				f[6] += "  |  Selected for next boot"
			}
			if s.Folder != "" {
				f[6] += "  |  " + s.Folder
			}
			if m.layout.Multi && s.ID == 1 {
				f[6] = "Shared kernel and drivers  |  OpenATV 8 or newer required for multiboot"
			}
			if s.Incomplete {
				f[6] = "Installation incomplete  |  Reinstall this image before booting"
			}
		}
	}
	if nandImage {
		switch m.view {
		case "device":
			f[11] = "ARROWS: previous / next page   |   INFO / BACK: overview"
		case "notice":
			f[11] = "BACK: overview"
		case "confirm":
		default:
			f[0], f[2] = "message", "Internal NAND image"
			f[5] = m.slotName(0)
			if s.ID == 0 {
				f[5] = "Internal image unavailable"
			}
			f[11] = "OK: start internal image   |   INFO: device information"
		}
	}
	for i, s := range f {
		s = strings.Map(func(r rune) rune {
			if r < 32 || r == 127 {
				return ' '
			}
			return r
		}, s)
		r := []rune(s)
		if len(r) > 150 {
			s = string(r[:147]) + "..."
		}
		f[i] = s
	}
	return strings.Join(f[:], "\t") + "\n"
}

// Only a second, distinct OK press in the confirmation view returns a boot ID.
func (m *displayMenu) key(k int) int {
	const up, down, left, right, enter, ok, info, back, exit = 103, 108, 105, 106, 28, 352, 358, 158, 174
	prev := k == up || k == left
	next := k == down || k == right
	accept := k == ok || k == enter
	if k == back || k == exit || k == 1 {
		m.view = ""
		return 0
	}
	switch m.view {
	case "notice":
		if accept {
			m.view = ""
		}
	case "confirm":
		if accept {
			m.view, m.notice = "notice", "Starting image ..."
			return m.slot().ID
		}
	case "device":
		if k == info {
			m.view = ""
		} else if len(m.pages) > 0 {
			if prev {
				m.page = (m.page + len(m.pages) - 1) % len(m.pages)
			}
			if next {
				m.page = (m.page + 1) % len(m.pages)
			}
		}
	default:
		if k == info || k == 139 { // INFO or MENU
			m.view, m.page = "device", 0
			m.pages = devicePages(deviceInfo())
		} else if accept {
			if m.slot().Installed {
				m.view = "confirm"
			} else {
				m.view, m.notice = "notice", "No bootable image found"
			}
		} else if count := len(m.layout.Slots); count > 0 && !nandImage {
			if prev {
				m.selected = (m.selected + count - 1) % count
			}
			if next {
				m.selected = (m.selected + 1) % count
			}
			if k >= 2 && k <= 10 {
				for i, s := range m.layout.Slots {
					if s.ID == k-1 {
						m.selected = i
					}
				}
			}
		}
	}
	return 0
}

func webAddress() string {
	if eth, err := net.InterfaceByName("eth0"); err == nil {
		aa, _ := eth.Addrs()
		for _, a := range aa {
			if ip, _, err := net.ParseCIDR(a.String()); err == nil && ip.To4() != nil {
				return "http://" + ip.String() + "/"
			}
		}
	}
	return ""
}

func startFromDisplay(id int) string {
	if !op.begin() {
		return "Another operation is in progress"
	}
	defer op.end()
	beginJob("start", "", "Starting image", id)
	l, err := layout()
	if err == nil {
		var s Slot
		s, err = lookup(l, id)
		if err == nil {
			err = startSlot(l, s)
		}
		if err == nil {
			err = reboot()
		}
	}
	if err != nil {
		job("error", err.Error())
		return err.Error()
	}
	return "Restarting Dreambox ..."
}

func runDisplay() error {
	cmd := exec.Command("/usr/sbin/odr-hdmi")
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer output.Close()
	if err = cmd.Start(); err != nil {
		return err
	}
	defer func() { input.Close(); cmd.Wait() }()
	keys := make(chan int)
	done := make(chan struct{})
	defer close(done)
	go func() {
		defer close(keys)
		sc := bufio.NewScanner(output)
		for sc.Scan() {
			value, ok := strings.CutPrefix(sc.Text(), "KEY ")
			k, e := strconv.Atoi(value)
			if ok && e == nil {
				select {
				case keys <- k:
				case <-done:
					return
				}
			}
		}
	}()
	var m displayMenu
	loaded := m.inventory()
	m.url = webAddress()
	last := ""
	lastJob := ""
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		frame := m.frame()
		if frame != last {
			if _, e := io.WriteString(input, frame); e != nil {
				return e
			}
			last = frame
		}
		select {
		case k, ok := <-keys:
			if !ok {
				return fmt.Errorf("HDMI helper exited")
			}
			if id := m.key(k); id > 0 {
				m.notice = startFromDisplay(id)
			}
		case <-tick.C:
			m.url = webAddress()
			jobMu.Lock()
			state := busyJob.State
			jobMu.Unlock()
			if state == "done" && lastJob != state {
				loaded = false
			}
			if !loaded && state != "working" {
				loaded = m.inventory()
			}
			lastJob = state
		}
	}
}

func superviseHDMI() {
	for {
		if err := runDisplay(); err != nil {
			fmt.Fprintln(os.Stderr, "ODR display:", err)
		}
		time.Sleep(10 * time.Second)
	}
}
