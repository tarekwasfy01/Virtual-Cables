package usbip

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"testing"
	"time"

	"virtualcables/internal/uac1"
)

func TestIsoCompletionsAreSerializedPerEndpoint(t *testing.T) {
	state := newConnectionState(io.Discard)
	now := time.Unix(100, 0)
	first := state.reserveIsoCompletion(2, 10, now)
	second := state.reserveIsoCompletion(2, 10, now)
	otherEndpoint := state.reserveIsoCompletion(1, 10, now)

	if got, want := first.Sub(now), 10*time.Millisecond; got != want {
		t.Fatalf("first deadline=%s, want %s", got, want)
	}
	if got, want := second.Sub(now), 20*time.Millisecond; got != want {
		t.Fatalf("second deadline=%s, want %s", got, want)
	}
	if got, want := otherEndpoint.Sub(now), 10*time.Millisecond; got != want {
		t.Fatalf("independent endpoint deadline=%s, want %s", got, want)
	}
}

func TestDeviceListEncoding(t *testing.T) {
	dev, err := uac1.NewDevice(1, 100)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer("127.0.0.1:0", []*uac1.Device{dev}, log.New(io.Discard, "", 0))
	var b bytes.Buffer
	if err := s.writeDeviceList(&b); err != nil {
		t.Fatal(err)
	}
	// 8-byte OP header, 4-byte count, 312-byte device, 12-byte interfaces.
	if b.Len() != 336 {
		t.Fatalf("length=%d, want 336", b.Len())
	}
	if got := binary.BigEndian.Uint32(b.Bytes()[8:12]); got != 1 {
		t.Fatalf("device count=%d", got)
	}
	if !bytes.Contains(b.Bytes(), []byte("1-1")) {
		t.Fatal("bus ID missing")
	}
}
