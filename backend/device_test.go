package main

import (
	"encoding/binary"
	"reflect"
	"testing"
)

func TestFactoryPublicFields(t *testing.T) {
	parsed := factoryFields("CA_DATE=20240229\nCA_SERIAL=42\nCA_ASERIAL=A-42\nCA_PRIVATE=omitted\n")
	want := map[string]string{"CA_DATE": "2024-02-29", "CA_SERIAL": "42", "CA_ASERIAL": "A-42"}
	if !reflect.DeepEqual(parsed, want) {
		t.Fatal(parsed)
	}
	if got := factoryFields("CA_DATE=20230229\nCA_SERIAL=-1\nCA_ASERIAL=$(bad)\n"); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestPhysicalMemoryBanks(t *testing.T) {
	banks := make([]byte, 32)
	binary.BigEndian.PutUint64(banks[8:], 1<<30)
	binary.BigEndian.PutUint64(banks[16:], 2<<30)
	binary.BigEndian.PutUint64(banks[24:], 1<<30)
	if got := memoryBanks(banks, 2, 2); got != 2<<30 {
		t.Fatal(got)
	}
	if memoryBanks(banks[:31], 2, 2) != 0 || memoryBanks(banks, 0, 2) != 0 {
		t.Fatal("invalid device tree memory properties accepted")
	}
}
