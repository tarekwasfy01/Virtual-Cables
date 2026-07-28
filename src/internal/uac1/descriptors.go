package uac1

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"
)

const (
	DescriptorDevice        = 0x01
	DescriptorConfiguration = 0x02
	DescriptorString        = 0x03
	DescriptorInterface     = 0x04
	DescriptorEndpoint      = 0x05
	DescriptorCSInterface   = 0x24
	DescriptorCSEndpoint    = 0x25
)

// Descriptors describes one full-speed USB Audio Class 1.0 cable. Windows uses
// its in-box usbaudio.sys class driver when this descriptor set enumerates.
type Descriptors struct {
	CableNumber int
	Product     string
	Serial      string
	Device      []byte
	Config      []byte
	Strings     map[uint8][]byte
}

func NewDescriptors(cableNumber int) (*Descriptors, error) {
	if cableNumber < 1 || cableNumber > 255 {
		return nil, fmt.Errorf("cable number must be 1..255")
	}
	product := fmt.Sprintf("Virtual Cable %02d", cableNumber)
	serial := fmt.Sprintf("VCABLE-%03d", cableNumber)

	d := &Descriptors{
		CableNumber: cableNumber,
		Product:     product,
		Serial:      serial,
		Strings:     make(map[uint8][]byte),
	}
	d.Device = deviceDescriptor(cableNumber)
	d.Config = configurationDescriptor()
	d.Strings[0] = []byte{4, DescriptorString, 0x09, 0x04} // en-US
	d.Strings[1] = stringDescriptor("Tarek Wasfy and AI")
	d.Strings[2] = stringDescriptor(product)
	d.Strings[3] = stringDescriptor(serial)
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return d, nil
}

func deviceDescriptor(cableNumber int) []byte {
	pid := uint16(0xCA00 + cableNumber)
	b := make([]byte, 18)
	b[0] = 18
	b[1] = DescriptorDevice
	binary.LittleEndian.PutUint16(b[2:4], 0x0110) // USB 1.1 full-speed device; no qualifier/BOS required
	b[4] = 0x00                                   // class defined per interface
	b[5] = 0x00
	b[6] = 0x00
	b[7] = 64                                      // EP0 packet size
	binary.LittleEndian.PutUint16(b[8:10], 0xFFFF) // experimental local VID
	binary.LittleEndian.PutUint16(b[10:12], pid)
	binary.LittleEndian.PutUint16(b[12:14], 0x0001)
	b[14] = 1 // manufacturer
	b[15] = 2 // product
	b[16] = 3 // serial
	b[17] = 1 // configurations
	return b
}

func configurationDescriptor() []byte {
	var b []byte
	appendBytes := func(x ...byte) { b = append(b, x...) }

	// Configuration header; wTotalLength is patched after construction.
	appendBytes(9, DescriptorConfiguration, 0, 0, 3, 1, 0, 0x80, 50)

	// AudioControl interface 0.
	appendBytes(9, DescriptorInterface, 0, 0, 0, 0x01, 0x01, 0x00, 0)
	// UAC1 AC Header: two AudioStreaming interfaces (1 and 2).
	appendBytes(10, DescriptorCSInterface, 0x01, 0x00, 0x01, 72, 0, 2, 1, 2)
	// Playback path: USB streaming input terminal -> feature -> speaker output.
	appendBytes(12, DescriptorCSInterface, 0x02, 1, 0x01, 0x01, 0, 2, 0x03, 0x00, 0, 0)
	appendBytes(10, DescriptorCSInterface, 0x06, 2, 1, 1, 0x03, 0x00, 0x00, 0)
	appendBytes(9, DescriptorCSInterface, 0x03, 3, 0x01, 0x03, 0, 2, 0)
	// Capture path: microphone input -> feature -> USB streaming output terminal.
	appendBytes(12, DescriptorCSInterface, 0x02, 4, 0x01, 0x02, 0, 2, 0x03, 0x00, 0, 0)
	appendBytes(10, DescriptorCSInterface, 0x06, 5, 4, 1, 0x03, 0x00, 0x00, 0)
	appendBytes(9, DescriptorCSInterface, 0x03, 6, 0x01, 0x01, 0, 5, 0)

	// Playback AudioStreaming interface 1, alternate setting 0 (zero bandwidth).
	appendBytes(9, DescriptorInterface, 1, 0, 0, 0x01, 0x02, 0x00, 0)
	// Alternate setting 1, stereo PCM 48 kHz / 16 bit, OUT endpoint 1.
	appendBytes(9, DescriptorInterface, 1, 1, 1, 0x01, 0x02, 0x00, 0)
	appendBytes(7, DescriptorCSInterface, 0x01, 1, 1, 0x01, 0x00)
	appendBytes(11, DescriptorCSInterface, 0x02, 1, 2, 2, 16, 1, 0x80, 0xBB, 0x00)
	appendBytes(9, DescriptorEndpoint, 0x01, 0x09, 0xC0, 0x00, 1, 0, 0)
	appendBytes(7, DescriptorCSEndpoint, 0x01, 0, 0, 0, 0)

	// Capture AudioStreaming interface 2, alternate setting 0.
	appendBytes(9, DescriptorInterface, 2, 0, 0, 0x01, 0x02, 0x00, 0)
	// Alternate setting 1, stereo PCM 48 kHz / 16 bit, synchronous IN endpoint 2.
	appendBytes(9, DescriptorInterface, 2, 1, 1, 0x01, 0x02, 0x00, 0)
	appendBytes(7, DescriptorCSInterface, 0x01, 6, 1, 0x01, 0x00)
	appendBytes(11, DescriptorCSInterface, 0x02, 1, 2, 2, 16, 1, 0x80, 0xBB, 0x00)
	appendBytes(9, DescriptorEndpoint, 0x82, 0x0D, 0xC0, 0x00, 1, 0, 0)
	appendBytes(7, DescriptorCSEndpoint, 0x01, 0, 0, 0, 0)

	binary.LittleEndian.PutUint16(b[2:4], uint16(len(b)))
	return b
}

func stringDescriptor(s string) []byte {
	encoded := utf16.Encode([]rune(s))
	length := 2 + len(encoded)*2
	if length > 255 {
		encoded = encoded[:126]
		length = 254
	}
	b := make([]byte, length)
	b[0] = byte(length)
	b[1] = DescriptorString
	for i, v := range encoded {
		binary.LittleEndian.PutUint16(b[2+i*2:], v)
	}
	return b
}

func (d *Descriptors) GetDescriptor(descriptorType, index uint8) ([]byte, bool) {
	switch descriptorType {
	case DescriptorDevice:
		if index == 0 {
			return append([]byte(nil), d.Device...), true
		}
	case DescriptorConfiguration:
		if index == 0 {
			return append([]byte(nil), d.Config...), true
		}
	case DescriptorString:
		v, ok := d.Strings[index]
		return append([]byte(nil), v...), ok
	}
	return nil, false
}

func (d *Descriptors) Validate() error {
	if len(d.Device) != 18 || d.Device[0] != 18 || d.Device[1] != DescriptorDevice {
		return fmt.Errorf("invalid device descriptor")
	}
	if len(d.Config) < 9 || d.Config[1] != DescriptorConfiguration {
		return fmt.Errorf("invalid configuration descriptor")
	}
	if got := int(binary.LittleEndian.Uint16(d.Config[2:4])); got != len(d.Config) {
		return fmt.Errorf("configuration wTotalLength=%d, actual=%d", got, len(d.Config))
	}
	for pos := 0; pos < len(d.Config); {
		length := int(d.Config[pos])
		if length < 2 || pos+length > len(d.Config) {
			return fmt.Errorf("invalid descriptor at offset %d", pos)
		}
		pos += length
	}
	return nil
}
