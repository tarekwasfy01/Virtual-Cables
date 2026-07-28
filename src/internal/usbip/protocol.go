package usbip

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

const (
	ProtocolVersion = 0x0111

	OpReqImport  = 0x8003
	OpRepImport  = 0x0003
	OpReqDevlist = 0x8005
	OpRepDevlist = 0x0005

	CmdSubmit = 0x00000001
	CmdUnlink = 0x00000002
	RetSubmit = 0x00000003
	RetUnlink = 0x00000004

	DirectionOut = 0
	DirectionIn  = 1

	SpeedFull = 2

	// The USB/IP wire protocol uses 0xffffffff for non-isochronous URBs.
	// It is not an error and must never be interpreted as a negative count.
	NoIsoPackets uint32 = 0xffffffff
)

const (
	StatusOK        int32 = 0
	StatusNoEntry   int32 = -2
	StatusInvalid   int32 = -22
	StatusPipe      int32 = -32
	StatusConnReset int32 = -104
)

type OpHeader struct {
	Version uint16
	Code    uint16
	Status  uint32
}

func readOpHeader(r io.Reader) (OpHeader, error) {
	var h OpHeader
	if err := binary.Read(r, binary.BigEndian, &h); err != nil {
		return h, err
	}
	if h.Version != ProtocolVersion {
		return h, fmt.Errorf("unsupported USB/IP version 0x%04x", h.Version)
	}
	return h, nil
}

func writeOpHeader(w io.Writer, code uint16, status uint32) error {
	return binary.Write(w, binary.BigEndian, OpHeader{Version: ProtocolVersion, Code: code, Status: status})
}

type BasicHeader struct {
	Command   uint32
	Sequence  uint32
	DeviceID  uint32
	Direction uint32
	Endpoint  uint32
}

type SubmitRequest struct {
	Basic                BasicHeader
	TransferFlags        uint32
	TransferBufferLength uint32
	StartFrame           uint32
	NumberOfPackets      uint32
	Interval             uint32
	Setup                [8]byte
}

type IsoPacket struct {
	Offset       uint32
	Length       uint32
	ActualLength uint32
	Status       int32
}

func (s SubmitRequest) IsIsochronous() bool {
	return s.NumberOfPackets != NoIsoPackets && s.NumberOfPackets != 0
}

func readBasicHeader(r io.Reader) (BasicHeader, error) {
	var h BasicHeader
	err := binary.Read(r, binary.BigEndian, &h)
	return h, err
}

func readSubmitBody(r io.Reader, basic BasicHeader) (SubmitRequest, error) {
	s := SubmitRequest{Basic: basic}
	if err := binary.Read(r, binary.BigEndian, &s.TransferFlags); err != nil {
		return s, err
	}
	if err := binary.Read(r, binary.BigEndian, &s.TransferBufferLength); err != nil {
		return s, err
	}
	if err := binary.Read(r, binary.BigEndian, &s.StartFrame); err != nil {
		return s, err
	}
	if err := binary.Read(r, binary.BigEndian, &s.NumberOfPackets); err != nil {
		return s, err
	}
	if err := binary.Read(r, binary.BigEndian, &s.Interval); err != nil {
		return s, err
	}
	_, err := io.ReadFull(r, s.Setup[:])
	return s, err
}

func readIsoPackets(r io.Reader, count uint32) ([]IsoPacket, error) {
	if count == NoIsoPackets || count == 0 {
		return nil, nil
	}
	if count > 4096 {
		return nil, fmt.Errorf("invalid isochronous packet count %d", count)
	}
	packets := make([]IsoPacket, int(count))
	for i := range packets {
		if err := binary.Read(r, binary.BigEndian, &packets[i]); err != nil {
			return nil, err
		}
	}
	return packets, nil
}

// writeRetSubmit writes one complete USBIP_RET_SUBMIT frame. The USB/IP
// specification requires devid, direction and endpoint to be zero in replies;
// only command and sequence identify the response.
func writeRetSubmit(w io.Writer, req SubmitRequest, status int32, actualLength uint32, data []byte, packets []IsoPacket, errorCount uint32) error {
	if status != StatusOK {
		actualLength = 0
		data = nil
	}

	numberOfPackets := NoIsoPackets
	startFrame := uint32(0)
	if req.IsIsochronous() {
		numberOfPackets = uint32(len(packets))
		startFrame = req.StartFrame
	}

	var frame bytes.Buffer
	basic := BasicHeader{
		Command:  RetSubmit,
		Sequence: req.Basic.Sequence,
		// DeviceID, Direction and Endpoint must be zero in server replies.
	}
	if err := binary.Write(&frame, binary.BigEndian, basic); err != nil {
		return err
	}
	fields := []any{status, actualLength, startFrame, numberOfPackets, errorCount, uint64(0)}
	for _, field := range fields {
		if err := binary.Write(&frame, binary.BigEndian, field); err != nil {
			return err
		}
	}
	if len(data) > 0 {
		if _, err := frame.Write(data); err != nil {
			return err
		}
	}
	for _, p := range packets {
		if err := binary.Write(&frame, binary.BigEndian, p); err != nil {
			return err
		}
	}
	return writeAll(w, frame.Bytes())
}

func writeRetUnlink(w io.Writer, request BasicHeader, status int32) error {
	var frame bytes.Buffer
	reply := BasicHeader{Command: RetUnlink, Sequence: request.Sequence}
	if err := binary.Write(&frame, binary.BigEndian, reply); err != nil {
		return err
	}
	if err := binary.Write(&frame, binary.BigEndian, status); err != nil {
		return err
	}
	frame.Write(make([]byte, 24))
	return writeAll(w, frame.Bytes())
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func fixedString(dst []byte, value string) {
	clear(dst)
	copy(dst, []byte(value))
}
