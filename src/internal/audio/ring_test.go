package audio

import (
	"bytes"
	"testing"
)

func TestRingRoundTrip(t *testing.T) {
	r := NewRing(16)
	in := []byte{1, 2, 3, 4}
	r.Write(in)
	out := make([]byte, 4)
	r.ReadSilence(out)
	if !bytes.Equal(in, out) {
		t.Fatalf("got %v, want %v", out, in)
	}
}

func TestRingOverrunKeepsNewest(t *testing.T) {
	r := NewRing(4)
	r.Write([]byte{1, 2, 3, 4, 5, 6})
	out := make([]byte, 4)
	r.ReadSilence(out)
	want := []byte{3, 4, 5, 6}
	if !bytes.Equal(out, want) {
		t.Fatalf("got %v, want %v", out, want)
	}
}

func TestRingPadsSilence(t *testing.T) {
	r := NewRing(4)
	r.Write([]byte{7, 8})
	out := make([]byte, 4)
	r.ReadSilence(out)
	want := []byte{7, 8, 0, 0}
	if !bytes.Equal(out, want) {
		t.Fatalf("got %v, want %v", out, want)
	}
}
