package usbip

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestOpHeaderRoundTrip(t *testing.T) {
	var b bytes.Buffer
	if err := writeOpHeader(&b, OpRepDevlist, 0); err != nil {
		t.Fatal(err)
	}
	h, err := readOpHeader(&b)
	if err != nil {
		t.Fatal(err)
	}
	if h.Version != ProtocolVersion || h.Code != OpRepDevlist || h.Status != 0 {
		t.Fatalf("unexpected header: %+v", h)
	}
}

func TestNoIsoPacketSentinelIsAccepted(t *testing.T) {
	packets, err := readIsoPackets(bytes.NewReader(nil), NoIsoPackets)
	if err != nil {
		t.Fatal(err)
	}
	if packets != nil {
		t.Fatalf("packets=%v, want nil", packets)
	}
}

func TestRetSubmitNonIsoWireLayout(t *testing.T) {
	var b bytes.Buffer
	req := SubmitRequest{
		Basic: BasicHeader{
			Sequence:  7,
			DeviceID:  0x00010001,
			Direction: DirectionIn,
			Endpoint:  3,
		},
		NumberOfPackets: NoIsoPackets,
	}
	if err := writeRetSubmit(&b, req, StatusOK, 4, []byte{1, 2, 3, 4}, nil, 0); err != nil {
		t.Fatal(err)
	}
	// 20-byte basic header + 28-byte body + 4-byte payload.
	if b.Len() != 52 {
		t.Fatalf("length=%d, want 52", b.Len())
	}
	wire := b.Bytes()
	if got := binary.BigEndian.Uint32(wire[0:4]); got != RetSubmit {
		t.Fatalf("command=%d", got)
	}
	if got := binary.BigEndian.Uint32(wire[4:8]); got != 7 {
		t.Fatalf("sequence=%d", got)
	}
	// Server replies must zero devid, direction and endpoint.
	for offset := 8; offset < 20; offset += 4 {
		if got := binary.BigEndian.Uint32(wire[offset : offset+4]); got != 0 {
			t.Fatalf("reply basic field at offset %d = %d, want 0", offset, got)
		}
	}
	if got := binary.BigEndian.Uint32(wire[32:36]); got != NoIsoPackets {
		t.Fatalf("number_of_packets=0x%08x, want 0xffffffff", got)
	}
}

func TestRetSubmitIsoWireLayout(t *testing.T) {
	var b bytes.Buffer
	req := SubmitRequest{
		Basic:           BasicHeader{Sequence: 9, Direction: DirectionIn, Endpoint: 2},
		StartFrame:      123,
		NumberOfPackets: 2,
	}
	packets := []IsoPacket{
		{Offset: 0, Length: 192, ActualLength: 192, Status: 0},
		{Offset: 192, Length: 192, ActualLength: 192, Status: 0},
	}
	data := make([]byte, 384)
	if err := writeRetSubmit(&b, req, StatusOK, 384, data, packets, 0); err != nil {
		t.Fatal(err)
	}
	wire := b.Bytes()
	if got := binary.BigEndian.Uint32(wire[28:32]); got != 123 {
		t.Fatalf("start_frame=%d", got)
	}
	if got := binary.BigEndian.Uint32(wire[32:36]); got != 2 {
		t.Fatalf("number_of_packets=%d", got)
	}
	if b.Len() != 48+384+2*16 {
		t.Fatalf("length=%d", b.Len())
	}
}
