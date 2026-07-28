package usbip

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"virtualcables/internal/uac1"
)

const maxTransferLength = 16 * 1024 * 1024

type Server struct {
	Address string
	Devices []*uac1.Device
	Logger  *log.Logger

	mu        sync.Mutex
	listener  net.Listener
	urbCount  atomic.Uint64
	ready     chan struct{}
	readyOnce sync.Once
}

func NewServer(address string, devices []*uac1.Device, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	return &Server{Address: address, Devices: devices, Logger: logger, ready: make(chan struct{})}
}

// Ready is closed after the TCP listener has been created successfully.
func (s *Server) Ready() <-chan struct{} { return s.ready }

func (s *Server) Serve(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.Address)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()
	s.readyOnce.Do(func() { close(s.ready) })
	defer ln.Close()

	s.Logger.Printf("USB/IP server listening on %s with %d device(s)", ln.Addr(), len(s.Devices))
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetNoDelay(true)
			_ = tcp.SetKeepAlive(true)
			_ = tcp.SetKeepAlivePeriod(15 * time.Second)
		}
		go s.handleConnection(ctx, conn)
	}
}

func (s *Server) handleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReaderSize(conn, 64*1024)
	header, err := readOpHeader(reader)
	if err != nil {
		s.Logger.Printf("management handshake from %s failed: %v", conn.RemoteAddr(), err)
		return
	}

	switch header.Code {
	case OpReqDevlist:
		s.Logger.Printf("device-list request from %s", conn.RemoteAddr())
		if err := s.writeDeviceList(conn); err != nil {
			s.Logger.Printf("device-list reply failed: %v", err)
		}
	case OpReqImport:
		busRaw := make([]byte, 32)
		if _, err := io.ReadFull(reader, busRaw); err != nil {
			s.Logger.Printf("import request failed: %v", err)
			return
		}
		busID := strings.TrimRight(string(busRaw), "\x00")
		device := s.findDevice(busID)
		if device == nil {
			_ = writeOpHeader(conn, OpRepImport, 1)
			s.Logger.Printf("client requested unknown bus ID %q", busID)
			return
		}
		if err := writeOpHeader(conn, OpRepImport, 0); err != nil {
			return
		}
		if err := writeUSBDevice(conn, device); err != nil {
			return
		}
		_ = conn.SetDeadline(time.Time{})
		s.Logger.Printf("import accepted: %s (%s), client %s", device.BusID, device.Product(), conn.RemoteAddr())
		s.handleURBs(ctx, reader, conn, device)
	default:
		s.Logger.Printf("unsupported management opcode 0x%04x from %s", header.Code, conn.RemoteAddr())
	}
}

func (s *Server) writeDeviceList(w io.Writer) error {
	if err := writeOpHeader(w, OpRepDevlist, 0); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, uint32(len(s.Devices))); err != nil {
		return err
	}
	for _, d := range s.Devices {
		if err := writeUSBDevice(w, d); err != nil {
			return err
		}
		// The devlist reply contains one class/subclass/protocol tuple for every
		// interface in the active configuration, not one tuple per alt setting.
		interfaces := [][4]byte{
			{0x01, 0x01, 0x00, 0}, // AudioControl
			{0x01, 0x02, 0x00, 0}, // AudioStreaming playback
			{0x01, 0x02, 0x00, 0}, // AudioStreaming capture
		}
		for _, iface := range interfaces {
			if err := writeAll(w, iface[:]); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeUSBDevice(w io.Writer, d *uac1.Device) error {
	var path [256]byte
	var busID [32]byte
	fixedString(path[:], fmt.Sprintf("/sys/devices/platform/virtual-cables/%s", d.BusID))
	fixedString(busID[:], d.BusID)
	if err := writeAll(w, path[:]); err != nil {
		return err
	}
	if err := writeAll(w, busID[:]); err != nil {
		return err
	}
	dev := d.Descriptors.Device
	fields := []any{
		uint32(1),         // bus number
		uint32(d.Number),  // device number
		uint32(SpeedFull), // USB full speed
		binary.LittleEndian.Uint16(dev[8:10]),
		binary.LittleEndian.Uint16(dev[10:12]),
		binary.LittleEndian.Uint16(dev[12:14]),
		dev[4], dev[5], dev[6],
		uint8(1), // active configuration
		dev[17],  // configurations
		uint8(3), // interfaces
	}
	for _, field := range fields {
		if err := binary.Write(w, binary.BigEndian, field); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) findDevice(busID string) *uac1.Device {
	for _, d := range s.Devices {
		if d.BusID == busID {
			return d
		}
	}
	return nil
}

type connectionState struct {
	writer io.Writer

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[uint32]context.CancelFunc
	isoNext map[uint32]time.Time
	wg      sync.WaitGroup
}

func newConnectionState(w io.Writer) *connectionState {
	return &connectionState{
		writer:  w,
		pending: make(map[uint32]context.CancelFunc),
		isoNext: make(map[uint32]time.Time),
	}
}

// reserveIsoCompletion assigns each queued URB its own point on the endpoint
// timeline. Windows queues several 10 ms URBs at once. Sleeping each goroutine
// for 10 ms independently releases the whole batch together and makes capture
// run roughly ten times faster than real time. Reserving consecutive deadlines
// preserves the USB full-speed frame cadence even with a deep host queue.
func (c *connectionState) reserveIsoCompletion(endpoint uint32, packets uint32, now time.Time) time.Time {
	duration := time.Duration(packets) * time.Millisecond
	if duration <= 0 {
		return now
	}
	if duration > 250*time.Millisecond {
		duration = 250 * time.Millisecond
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	base := c.isoNext[endpoint]
	if base.Before(now) {
		base = now
	}
	deadline := base.Add(duration)
	c.isoNext[endpoint] = deadline
	return deadline
}

func (c *connectionState) writeSubmit(req SubmitRequest, status int32, actual uint32, data []byte, packets []IsoPacket, errorCount uint32) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeRetSubmit(c.writer, req, status, actual, data, packets, errorCount)
}

func (c *connectionState) writeUnlink(req BasicHeader, status int32) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeRetUnlink(c.writer, req, status)
}

func (c *connectionState) addPending(sequence uint32, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous := c.pending[sequence]; previous != nil {
		previous()
	}
	c.pending[sequence] = cancel
}

func (c *connectionState) removePending(sequence uint32) {
	c.mu.Lock()
	delete(c.pending, sequence)
	c.mu.Unlock()
}

func (c *connectionState) cancelPending(sequence uint32) bool {
	c.mu.Lock()
	cancel := c.pending[sequence]
	c.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (c *connectionState) cancelAll() {
	c.mu.Lock()
	for _, cancel := range c.pending {
		cancel()
	}
	c.mu.Unlock()
}

type parsedSubmit struct {
	req        SubmitRequest
	out        []byte
	packets    []IsoPacket
	completeAt time.Time
}

func readSubmit(r io.Reader, basic BasicHeader) (parsedSubmit, error) {
	req, err := readSubmitBody(r, basic)
	if err != nil {
		return parsedSubmit{}, err
	}
	if req.TransferBufferLength > maxTransferLength {
		return parsedSubmit{}, fmt.Errorf("invalid transfer length %d", req.TransferBufferLength)
	}

	var out []byte
	if basic.Direction == DirectionOut && req.TransferBufferLength > 0 {
		out = make([]byte, int(req.TransferBufferLength))
		if _, err := io.ReadFull(r, out); err != nil {
			return parsedSubmit{}, err
		}
	}
	packets, err := readIsoPackets(r, req.NumberOfPackets)
	if err != nil {
		return parsedSubmit{}, err
	}
	return parsedSubmit{req: req, out: out, packets: packets}, nil
}

func (s *Server) handleURBs(ctx context.Context, r *bufio.Reader, w io.Writer, device *uac1.Device) {
	urbCtx, cancelAll := context.WithCancel(ctx)
	state := newConnectionState(w)
	defer func() {
		cancelAll()
		state.cancelAll()
		state.wg.Wait()
		s.Logger.Printf("USB/IP session closed for %s", device.BusID)
	}()

	for {
		basic, err := readBasicHeader(r)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.Logger.Printf("%s disconnected: %v", device.BusID, err)
			}
			return
		}

		switch basic.Command {
		case CmdSubmit:
			submit, err := readSubmit(r, basic)
			if err != nil {
				s.Logger.Printf("%s could not parse SUBMIT seq=%d: %v", device.BusID, basic.Sequence, err)
				return
			}
			s.traceSubmit(device, submit)

			// Control traffic is deliberately serialized. Descriptor and state
			// requests on endpoint zero must retain USB ordering during enumeration.
			if basic.Endpoint == 0 || !submit.req.IsIsochronous() {
				if err := s.processSubmit(urbCtx, state, device, submit); err != nil {
					s.Logger.Printf("%s SUBMIT seq=%d failed: %v", device.BusID, basic.Sequence, err)
					return
				}
				continue
			}

			requestCtx, requestCancel := context.WithCancel(urbCtx)
			submit.completeAt = state.reserveIsoCompletion(basic.Endpoint, submit.req.NumberOfPackets, time.Now())
			state.addPending(basic.Sequence, requestCancel)
			state.wg.Add(1)
			go func(job parsedSubmit) {
				defer state.wg.Done()
				defer requestCancel()
				defer state.removePending(job.req.Basic.Sequence)
				if err := s.processSubmit(requestCtx, state, device, job); err != nil && requestCtx.Err() == nil {
					s.Logger.Printf("%s asynchronous SUBMIT seq=%d failed: %v", device.BusID, job.req.Basic.Sequence, err)
				}
			}(submit)

		case CmdUnlink:
			var target uint32
			if err := binary.Read(r, binary.BigEndian, &target); err != nil {
				return
			}
			reserved := make([]byte, 24)
			if _, err := io.ReadFull(r, reserved); err != nil {
				return
			}
			found := state.cancelPending(target)
			status := StatusOK
			if found {
				// The protocol specifies -ECONNRESET for a successful unlink.
				status = StatusConnReset
			}
			s.Logger.Printf("%s UNLINK seq=%d target=%d found=%t", device.BusID, basic.Sequence, target, found)
			if err := state.writeUnlink(basic, status); err != nil {
				return
			}

		default:
			s.Logger.Printf("%s unsupported URB command 0x%08x", device.BusID, basic.Command)
			return
		}
	}
}

func (s *Server) processSubmit(ctx context.Context, state *connectionState, device *uac1.Device, submit parsedSubmit) error {
	req := submit.req

	if req.Basic.Endpoint == 0 {
		setup, err := uac1.ParseSetup(req.Setup[:])
		if err != nil {
			return state.writeSubmit(req, StatusInvalid, 0, nil, nil, 0)
		}
		data, status := device.HandleControl(setup, submit.out)
		if uint32(len(data)) > req.TransferBufferLength {
			data = data[:req.TransferBufferLength]
		}
		actual := uint32(len(data))
		if req.Basic.Direction == DirectionOut && status == StatusOK {
			actual = uint32(len(submit.out))
			data = nil
		}
		return state.writeSubmit(req, status, actual, data, nil, 0)
	}

	if req.IsIsochronous() {
		select {
		case <-ctx.Done():
			markIsoError(submit.packets, StatusConnReset)
			return state.writeSubmit(req, StatusConnReset, 0, nil, submit.packets, uint32(len(submit.packets)))
		default:
		}
	}

	switch {
	case req.Basic.Endpoint == 1 && req.Basic.Direction == DirectionOut:
		// Windows can queue the first isochronous request immediately around
		// SET_INTERFACE. Do not tear down the device for this harmless race.
		// Accept and discard the packet until the playback interface is active.
		actual := len(submit.out)
		if device.PlaybackActive() {
			actual = device.WritePlayback(submit.out)
		}
		markIsoPackets(submit.packets, actual)
		if req.IsIsochronous() {
			if err := waitIsoCompletion(ctx, submit.completeAt); err != nil {
				markIsoError(submit.packets, StatusConnReset)
				return state.writeSubmit(req, StatusConnReset, 0, nil, submit.packets, uint32(len(submit.packets)))
			}
		}
		return state.writeSubmit(req, StatusOK, uint32(actual), nil, submit.packets, 0)

	case req.Basic.Endpoint == 2 && req.Basic.Direction == DirectionIn:
		if req.IsIsochronous() {
			if err := waitIsoCompletion(ctx, submit.completeAt); err != nil {
				markIsoError(submit.packets, StatusConnReset)
				return state.writeSubmit(req, StatusConnReset, 0, nil, submit.packets, uint32(len(submit.packets)))
			}
		}
		length := responsePayloadLength(req, submit.packets)
		data := make([]byte, length)
		if device.CaptureActive() {
			device.ReadCapture(data)
		} // otherwise the zero-filled buffer is valid digital silence
		markIsoPackets(submit.packets, len(data))
		return state.writeSubmit(req, StatusOK, uint32(len(data)), data, submit.packets, 0)

	default:
		markIsoError(submit.packets, StatusPipe)
		return state.writeSubmit(req, StatusPipe, 0, nil, submit.packets, uint32(len(submit.packets)))
	}
}

func waitIsoCompletion(ctx context.Context, completeAt time.Time) error {
	if completeAt.IsZero() {
		return nil
	}
	duration := time.Until(completeAt)
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func responsePayloadLength(req SubmitRequest, packets []IsoPacket) int {
	if len(packets) == 0 {
		return int(req.TransferBufferLength)
	}
	var total uint64
	for _, p := range packets {
		total += uint64(p.Length)
	}
	if req.TransferBufferLength > 0 && total > uint64(req.TransferBufferLength) {
		total = uint64(req.TransferBufferLength)
	}
	if total > maxTransferLength {
		total = maxTransferLength
	}
	return int(total)
}

func markIsoPackets(packets []IsoPacket, actualTotal int) {
	remaining := actualTotal
	for i := range packets {
		packets[i].Status = StatusOK
		wanted := int(packets[i].Length)
		if wanted > remaining {
			wanted = remaining
		}
		if wanted < 0 {
			wanted = 0
		}
		packets[i].ActualLength = uint32(wanted)
		remaining -= wanted
	}
}

func markIsoError(packets []IsoPacket, status int32) {
	for i := range packets {
		packets[i].ActualLength = 0
		packets[i].Status = status
	}
}

func (s *Server) traceSubmit(device *uac1.Device, submit parsedSubmit) {
	req := submit.req
	if req.Basic.Endpoint == 0 {
		setup, _ := uac1.ParseSetup(req.Setup[:])
		s.Logger.Printf("%s CTRL seq=%d dir=%s %s", device.BusID, req.Basic.Sequence, directionName(req.Basic.Direction), describeSetup(setup))
		return
	}
	count := s.urbCount.Add(1)
	if count <= 20 || count%500 == 0 {
		s.Logger.Printf("%s URB #%d seq=%d ep=%d dir=%s bytes=%d iso_packets=%s", device.BusID, count, req.Basic.Sequence, req.Basic.Endpoint, directionName(req.Basic.Direction), req.TransferBufferLength, packetCountName(req.NumberOfPackets))
	}
}

func directionName(direction uint32) string {
	if direction == DirectionIn {
		return "IN"
	}
	return "OUT"
}

func packetCountName(count uint32) string {
	if count == NoIsoPackets {
		return "none"
	}
	return fmt.Sprintf("%d", count)
}

func describeSetup(s uac1.SetupPacket) string {
	return fmt.Sprintf("bmRequestType=0x%02X bRequest=0x%02X wValue=0x%04X wIndex=0x%04X wLength=%d", s.RequestType, s.Request, s.Value, s.Index, s.Length)
}
