package main

import (
	"strings"
	"testing"
)

func TestHDMISlotConfirmation(t *testing.T) {
	if nandImage {
		m := displayMenu{layout: Layout{Slots: []Slot{{ID: 1, Installed: true}}}}
		m.key(3)
		if m.selected != 0 || !strings.Contains(m.frame(), "Internal NAND image") || m.key(352) != 0 || m.key(352) != 1 {
			t.Fatal("Internal image requires two OK presses and no slot selection")
		}
		return
	}
	m := displayMenu{layout: Layout{Multi: true, Slots: []Slot{{ID: 1, Installed: true}, {ID: 3}, {ID: 7, Installed: true}}}}
	if m.key(103) != 0 || m.slot().ID != 7 {
		t.Fatal("wrap to last slot")
	}
	if m.key(352) != 0 || m.view != "confirm" {
		t.Fatal("first OK must only prompt")
	}
	if m.key(158) != 0 || m.view != "" {
		t.Fatal("cancel")
	}
	m.key(4)
	m.key(352)
	if m.view != "notice" || m.key(352) != 0 {
		t.Fatal("empty slot must not boot")
	}
	m.key(2)
	m.key(352)
	if m.key(352) != 1 {
		t.Fatal("explicit boot of selected slot")
	}
}

func TestHDMIDevicePages(t *testing.T) {
	fields := []DeviceField{{"a", "1"}, {"b", "2"}, {"c", "3"}, {"d", "4"}, {"e", "x\ty\n"}}
	m := displayMenu{view: "device", pages: devicePages([]DeviceGroup{{"System", fields}})}
	m.key(106)
	line := strings.TrimSuffix(m.frame(), "\n")
	if len(strings.Split(line, "\t")) != 12 || strings.Contains(line, "\n") || !strings.Contains(line, "x y") {
		t.Fatal("invalid frame")
	}
	m.key(105)
	if m.page != 0 || m.key(358) != 0 || m.view != "" {
		t.Fatal("device navigation")
	}
}
