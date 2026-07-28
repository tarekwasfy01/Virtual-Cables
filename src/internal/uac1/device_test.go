package uac1

import (
	"encoding/binary"
	"testing"
)

func TestGetDeviceDescriptor(t *testing.T) {
	d, err := NewDevice(1, 100)
	if err != nil {
		t.Fatal(err)
	}
	data, status := d.HandleControl(SetupPacket{
		RequestType: 0x80,
		Request:     RequestGetDescriptor,
		Value:       uint16(DescriptorDevice) << 8,
		Length:      18,
	}, nil)
	if status != 0 || len(data) != 18 {
		t.Fatalf("status=%d length=%d", status, len(data))
	}
}

func TestSetAndGetInterface(t *testing.T) {
	d, _ := NewDevice(1, 100)
	d.HandleControl(SetupPacket{RequestType: 0x00, Request: RequestSetConfiguration, Value: 1}, nil)
	d.HandleControl(SetupPacket{RequestType: 0x01, Request: RequestSetInterface, Index: 1, Value: 1}, nil)
	data, status := d.HandleControl(SetupPacket{RequestType: 0x81, Request: RequestGetInterface, Index: 1, Length: 1}, nil)
	if status != 0 || len(data) != 1 || data[0] != 1 {
		t.Fatalf("status=%d data=%v", status, data)
	}
}

func TestSamplingRate(t *testing.T) {
	d, _ := NewDevice(1, 100)
	data, status := d.HandleControl(SetupPacket{RequestType: 0xA2, Request: AudioGetCur, Value: 0x0100, Index: 0x82, Length: 3}, nil)
	if status != 0 || len(data) != 3 {
		t.Fatalf("status=%d data=%v", status, data)
	}
	rate := uint32(data[0]) | uint32(data[1])<<8 | uint32(data[2])<<16
	if rate != 48000 {
		t.Fatalf("rate=%d", rate)
	}
	_ = binary.LittleEndian
}
