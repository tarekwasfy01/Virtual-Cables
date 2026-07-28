package uac1

import (
	"encoding/binary"
	"testing"
)

func TestDescriptorsValidate(t *testing.T) {
	d, err := NewDescriptors(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := binary.LittleEndian.Uint16(d.Config[2:4]); int(got) != len(d.Config) {
		t.Fatalf("total length %d, actual %d", got, len(d.Config))
	}
	if got := binary.LittleEndian.Uint16(d.Device[2:4]); got != 0x0110 {
		t.Fatalf("bcdUSB=0x%04x, want 0x0110", got)
	}
}

func TestAudioEndpointDescriptors(t *testing.T) {
	d, err := NewDescriptors(1)
	if err != nil {
		t.Fatal(err)
	}
	var endpoints [][]byte
	for pos := 0; pos < len(d.Config); {
		length := int(d.Config[pos])
		if d.Config[pos+1] == DescriptorEndpoint {
			endpoints = append(endpoints, d.Config[pos:pos+length])
		}
		pos += length
	}
	if len(endpoints) != 2 {
		t.Fatalf("endpoint count=%d, want 2", len(endpoints))
	}
	if endpoints[0][2] != 0x01 || endpoints[0][3] != 0x09 {
		t.Fatalf("playback endpoint=% x", endpoints[0])
	}
	if endpoints[1][2] != 0x82 || endpoints[1][3] != 0x0D {
		t.Fatalf("capture endpoint=% x", endpoints[1])
	}
	for _, ep := range endpoints {
		if got := binary.LittleEndian.Uint16(ep[4:6]); got != 192 {
			t.Fatalf("wMaxPacketSize=%d, want 192", got)
		}
		if ep[6] != 1 {
			t.Fatalf("bInterval=%d, want 1", ep[6])
		}
	}
}

func TestUniqueProductID(t *testing.T) {
	a, _ := NewDescriptors(1)
	b, _ := NewDescriptors(2)
	pa := binary.LittleEndian.Uint16(a.Device[10:12])
	pb := binary.LittleEndian.Uint16(b.Device[10:12])
	if pa == pb {
		t.Fatal("product IDs must differ")
	}
}

func TestCaptureEndpointUsesWindowsCompatibleSynchronousMode(t *testing.T) {
	d, err := NewDescriptors(1)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for pos := 0; pos < len(d.Config); {
		length := int(d.Config[pos])
		if length < 2 || pos+length > len(d.Config) {
			t.Fatalf("invalid descriptor at %d", pos)
		}
		if d.Config[pos+1] == DescriptorEndpoint && length >= 4 && d.Config[pos+2] == 0x82 {
			found = true
			if got := d.Config[pos+3]; got != 0x0D {
				t.Fatalf("capture endpoint attributes = 0x%02X, want 0x0D", got)
			}
		}
		pos += length
	}
	if !found {
		t.Fatal("capture endpoint 0x82 not found")
	}
}
