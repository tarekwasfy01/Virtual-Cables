package uac1

import (
	"encoding/binary"
	"fmt"
	"sync"

	"virtualcables/internal/audio"
)

const (
	RequestGetStatus        = 0x00
	RequestClearFeature     = 0x01
	RequestSetFeature       = 0x03
	RequestSetAddress       = 0x05
	RequestGetDescriptor    = 0x06
	RequestSetDescriptor    = 0x07
	RequestGetConfiguration = 0x08
	RequestSetConfiguration = 0x09
	RequestGetInterface     = 0x0A
	RequestSetInterface     = 0x0B
	RequestSynchFrame       = 0x0C

	AudioSetCur = 0x01
	AudioGetCur = 0x81
	AudioGetMin = 0x82
	AudioGetMax = 0x83
	AudioGetRes = 0x84
)

type SetupPacket struct {
	RequestType uint8
	Request     uint8
	Value       uint16
	Index       uint16
	Length      uint16
}

func ParseSetup(b []byte) (SetupPacket, error) {
	if len(b) < 8 {
		return SetupPacket{}, fmt.Errorf("setup packet too short")
	}
	return SetupPacket{
		RequestType: b[0],
		Request:     b[1],
		Value:       binary.LittleEndian.Uint16(b[2:4]),
		Index:       binary.LittleEndian.Uint16(b[4:6]),
		Length:      binary.LittleEndian.Uint16(b[6:8]),
	}, nil
}

type Device struct {
	Number      int
	BusID       string
	Descriptors *Descriptors
	Buffer      *audio.Ring

	mu            sync.Mutex
	configuration uint8
	altSetting    map[uint8]uint8
	sampleRate    uint32
	mute          bool
	volume        int16
}

func NewDevice(number int, latencyMS int) (*Device, error) {
	d, err := NewDescriptors(number)
	if err != nil {
		return nil, err
	}
	if latencyMS < 10 {
		latencyMS = 10
	}
	if latencyMS > 2000 {
		latencyMS = 2000
	}
	// 48 kHz * 2 channels * 2 bytes/sample.
	capacity := 48000 * 2 * 2 * latencyMS / 1000
	return &Device{
		Number:      number,
		BusID:       fmt.Sprintf("1-%d", number),
		Descriptors: d,
		Buffer:      audio.NewRing(capacity),
		altSetting:  map[uint8]uint8{0: 0, 1: 0, 2: 0},
		sampleRate:  48000,
		volume:      0,
	}, nil
}

func (d *Device) Product() string { return d.Descriptors.Product }

// HandleControl processes the subset of standard and UAC1 class requests used
// during Windows enumeration and basic PCM streaming setup.
func (d *Device) HandleControl(setup SetupPacket, out []byte) (data []byte, status int32) {
	d.mu.Lock()
	defer d.mu.Unlock()

	standard := setup.RequestType&0x60 == 0x00
	if standard {
		switch setup.Request {
		case RequestGetDescriptor:
			typ := uint8(setup.Value >> 8)
			idx := uint8(setup.Value)
			v, ok := d.Descriptors.GetDescriptor(typ, idx)
			if !ok {
				return nil, -32 // EPIPE / STALL
			}
			return truncate(v, setup.Length), 0
		case RequestSetAddress:
			return nil, 0
		case RequestSetConfiguration:
			configuration := uint8(setup.Value)
			if configuration > 1 {
				return nil, -32
			}
			d.configuration = configuration
			// Selecting a configuration resets every interface to alternate 0.
			d.altSetting[0], d.altSetting[1], d.altSetting[2] = 0, 0, 0
			d.Buffer.Reset()
			return nil, 0
		case RequestGetConfiguration:
			return truncate([]byte{d.configuration}, setup.Length), 0
		case RequestSetInterface:
			iface := uint8(setup.Index)
			alt := uint8(setup.Value)
			if d.configuration != 1 || iface > 2 || alt > 1 || (iface == 0 && alt != 0) {
				return nil, -32
			}
			d.altSetting[iface] = alt
			if alt == 0 && (iface == 1 || iface == 2) {
				d.Buffer.Reset()
			}
			return nil, 0
		case RequestGetInterface:
			iface := uint8(setup.Index)
			alt, ok := d.altSetting[iface]
			if !ok {
				return nil, -32
			}
			return truncate([]byte{alt}, setup.Length), 0
		case RequestGetStatus:
			return truncate([]byte{0, 0}, setup.Length), 0
		case RequestSynchFrame:
			// Full-speed isochronous endpoints use one frame per millisecond.
			// A stable zero frame is sufficient for this synthetic device and is
			// preferable to stalling a host-side synchronization query.
			return truncate([]byte{0, 0}, setup.Length), 0
		case RequestClearFeature, RequestSetFeature:
			return nil, 0
		default:
			return nil, -32
		}
	}

	// UAC1 endpoint sampling-frequency requests.
	recipient := setup.RequestType & 0x1f
	selector := uint8(setup.Value >> 8)
	endpointAddress := uint8(setup.Index)
	if recipient == 0x02 && selector == 0x01 && (endpointAddress == 0x01 || endpointAddress == 0x82) { // endpoint, SAMPLING_FREQ_CONTROL
		switch setup.Request {
		case AudioSetCur:
			if len(out) >= 3 {
				rate := uint32(out[0]) | uint32(out[1])<<8 | uint32(out[2])<<16
				if rate != 48000 {
					return nil, -32
				}
				d.sampleRate = rate
			}
			return nil, 0
		case AudioGetCur:
			return truncate(rate24(d.sampleRate), setup.Length), 0
		case AudioGetMin, AudioGetMax:
			return truncate(rate24(48000), setup.Length), 0
		case AudioGetRes:
			return truncate(rate24(1), setup.Length), 0
		}
	}

	// Feature-unit mute/volume requests. Windows may query these because the
	// topology advertises a basic feature unit.
	entityID := uint8(setup.Index >> 8)
	interfaceNumber := uint8(setup.Index)
	if recipient == 0x01 && interfaceNumber == 0 && (entityID == 2 || entityID == 5) { // interface/entity
		controlSelector := uint8(setup.Value >> 8)
		switch controlSelector {
		case 0x01: // MUTE_CONTROL
			switch setup.Request {
			case AudioSetCur:
				if len(out) > 0 {
					d.mute = out[0] != 0
				}
				return nil, 0
			case AudioGetCur:
				if d.mute {
					return truncate([]byte{1}, setup.Length), 0
				}
				return truncate([]byte{0}, setup.Length), 0
			}
		case 0x02: // VOLUME_CONTROL, signed 1/256 dB
			switch setup.Request {
			case AudioSetCur:
				if len(out) >= 2 {
					d.volume = int16(binary.LittleEndian.Uint16(out[:2]))
				}
				return nil, 0
			case AudioGetCur:
				return truncate(int16LE(d.volume), setup.Length), 0
			case AudioGetMin:
				return truncate(int16LE(-60*256), setup.Length), 0
			case AudioGetMax:
				return truncate(int16LE(0), setup.Length), 0
			case AudioGetRes:
				return truncate(int16LE(256), setup.Length), 0
			}
		}
	}

	return nil, -32
}

func (d *Device) PlaybackActive() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.configuration == 1 && d.altSetting[1] == 1
}

func (d *Device) CaptureActive() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.configuration == 1 && d.altSetting[2] == 1
}

func (d *Device) WritePlayback(p []byte) int {
	if !d.PlaybackActive() {
		return 0
	}
	return d.Buffer.Write(p)
}

func (d *Device) ReadCapture(p []byte) int {
	if !d.CaptureActive() {
		for i := range p {
			p[i] = 0
		}
		return 0
	}
	return d.Buffer.ReadSilence(p)
}

func truncate(b []byte, n uint16) []byte {
	if int(n) < len(b) {
		b = b[:n]
	}
	return b
}

func rate24(rate uint32) []byte {
	return []byte{byte(rate), byte(rate >> 8), byte(rate >> 16)}
}

func int16LE(v int16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(v))
	return b
}
