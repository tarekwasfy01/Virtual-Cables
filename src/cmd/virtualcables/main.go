package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"virtualcables/internal/uac1"
	"virtualcables/internal/usbip"
)

var version = "0.5.13"

type appServer struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	done      chan error
	count     int
	address   string
	bufferMS  int
	logger    *log.Logger
	logPath   string
	lastError string
}

func (s *appServer) Restart(count int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stopLocked()

	devices, err := makeDevices(count, s.bufferMS)
	if err != nil {
		s.setErrorLocked(err)
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	server := usbip.NewServer(s.address, devices, s.logger)
	result := make(chan error, 1)
	go func() { result <- server.Serve(ctx) }()

	select {
	case <-server.Ready():
		s.cancel = cancel
		s.done = result
		s.count = count
		s.lastError = ""
		s.logger.Printf("Virtual Cables %s started with %d cable(s)", version, count)
		return nil
	case err := <-result:
		cancel()
		if err == nil {
			err = fmt.Errorf("USB/IP server stopped before becoming ready")
		}
		s.setErrorLocked(err)
		return err
	case <-time.After(3 * time.Second):
		cancel()
		err := fmt.Errorf("timed out while opening USB/IP port %s", s.address)
		s.setErrorLocked(err)
		return err
	}
}

func (s *appServer) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

func (s *appServer) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastError
}

func (s *appServer) LogPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logPath
}

func (s *appServer) setErrorLocked(err error) {
	if err == nil {
		s.lastError = ""
		return
	}
	s.lastError = err.Error()
	if s.logger != nil {
		s.logger.Printf("USB/IP server error: %v", err)
	}
}

func (s *appServer) stopLocked() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	if s.done != nil {
		select {
		case err := <-s.done:
			if err != nil && s.logger != nil {
				s.logger.Printf("USB/IP server stopped with error: %v", err)
			}
		case <-time.After(2 * time.Second):
			if s.logger != nil {
				s.logger.Printf("USB/IP server shutdown timed out; continuing restart")
			}
		}
	}
	s.cancel = nil
	s.done = nil
}

func main() {
	logger, logFile, logPath := newApplicationLogger()
	if logFile != nil {
		defer logFile.Close()
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			message := fmt.Sprintf("Unexpected startup failure: %v", recovered)
			logger.Printf("PANIC: %s", message)
			showFatalDialog(message + "\n\nDiagnostic log:\n" + logPath)
		}
	}()

	logger.Printf("============================================================")
	logger.Printf("Virtual Cables %s (%s/%s)", version, runtime.GOOS, runtime.GOARCH)
	logger.Printf("Process started. Executable: %s", executablePath())
	logger.Printf("Log file: %s", logPath)
	logger.Printf("USB/IP protocol: 0x%04X", usbip.ProtocolVersion)

	address := flag.String("listen", "127.0.0.1:3240", "USB/IP listen address")
	cables := flag.Int("cables", loadCableCount(), "number of virtual cable devices (1-32)")
	latencyMS := flag.Int("buffer-ms", 250, "audio ring-buffer capacity in milliseconds")
	selfTest := flag.Bool("self-test", false, "validate descriptors and ring buffers, then exit")
	dump := flag.Bool("dump-descriptors", false, "print USB descriptors, then exit")
	showVer := flag.Bool("version", false, "print version, then exit")
	attachBroker := flag.Bool("attach-broker", false, "run the elevated cable-device broker")
	brokerAddress := flag.String("broker-address", "", "internal broker callback address")
	brokerToken := flag.String("broker-token", "", "internal broker authentication token")
	flag.Parse()

	if *showVer {
		fmt.Printf("Virtual Cables %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}
	if *cables < 1 || *cables > 32 {
		logger.Printf("Invalid cable count %d; falling back to 1", *cables)
		*cables = 1
	}
	if *latencyMS < 20 || *latencyMS > 5000 {
		logger.Printf("Invalid buffer size %d ms; falling back to 250 ms", *latencyMS)
		*latencyMS = 250
	}
	if *attachBroker {
		if err := runAttachBroker(*brokerAddress, *brokerToken, logger); err != nil {
			logger.Printf("Administrator broker failed: %v", err)
			os.Exit(1)
		}
		return
	}

	devices, err := makeDevices(*cables, *latencyMS)
	if err != nil {
		failAndExit(logger, logPath, "Could not create the virtual cable devices", err, *selfTest || *dump)
	}
	if *selfTest {
		if err := runSelfTest(devices); err != nil {
			failAndExit(logger, logPath, "Self-test failed", err, true)
		}
		fmt.Printf("Virtual Cables %s self-test passed.\n", version)
		logger.Printf("Self-test passed")
		return
	}
	if *dump {
		for _, dev := range devices {
			fmt.Printf("\n[%s | bus ID %s]\n", dev.Product(), dev.BusID)
			fmt.Print(hex.Dump(dev.Descriptors.Device))
			fmt.Print(hex.Dump(dev.Descriptors.Config))
		}
		return
	}

	manager := &appServer{
		address:  *address,
		bufferMS: *latencyMS,
		logger:   logger,
		logPath:  logPath,
		count:    *cables,
	}
	if err := manager.Restart(*cables); err != nil {
		// Do not close the GUI. The window remains available so the user can
		// install the driver, inspect the status and open the diagnostic log.
		logger.Printf("Initial USB/IP server start failed; keeping GUI open: %v", err)
	}
	defer manager.Stop()
	runGUI(manager)
	logger.Printf("Virtual Cables window closed normally")
}

func failAndExit(logger *log.Logger, logPath, title string, err error, consoleOnly bool) {
	message := title
	if err != nil {
		message += ": " + err.Error()
	}
	logger.Printf("FATAL: %s", message)
	fmt.Fprintln(os.Stderr, "[ERROR] "+message)
	if !consoleOnly {
		showFatalDialog(message + "\n\nDiagnostic log:\n" + logPath)
	}
	os.Exit(1)
}

func makeDevices(count, latency int) ([]*uac1.Device, error) {
	devices := make([]*uac1.Device, 0, count)
	for i := 1; i <= count; i++ {
		d, err := uac1.NewDevice(i, latency)
		if err != nil {
			return nil, fmt.Errorf("create cable %d: %w", i, err)
		}
		devices = append(devices, d)
	}
	return devices, nil
}

func runSelfTest(devices []*uac1.Device) error {
	for _, dev := range devices {
		if err := dev.Descriptors.Validate(); err != nil {
			return fmt.Errorf("%s: %w", dev.Product(), err)
		}
		if _, status := dev.HandleControl(uac1.SetupPacket{RequestType: 0x00, Request: uac1.RequestSetConfiguration, Value: 1}, nil); status != 0 {
			return fmt.Errorf("set configuration status %d", status)
		}
		for _, iface := range []uint16{1, 2} {
			if _, status := dev.HandleControl(uac1.SetupPacket{RequestType: 0x01, Request: uac1.RequestSetInterface, Index: iface, Value: 1}, nil); status != 0 {
				return fmt.Errorf("activate interface status %d", status)
			}
		}
		pcm := []byte{0x11, 0x22, 0x33, 0x44}
		if dev.WritePlayback(pcm) != len(pcm) {
			return fmt.Errorf("PCM write failed")
		}
		out := make([]byte, len(pcm))
		dev.ReadCapture(out)
		for i := range pcm {
			if out[i] != pcm[i] {
				return fmt.Errorf("PCM loopback mismatch")
			}
		}
	}
	return nil
}

func applicationDataDir() string {
	if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
		return filepath.Join(local, "Virtual Cables")
	}
	base, err := os.UserCacheDir()
	if err == nil && strings.TrimSpace(base) != "" {
		return filepath.Join(base, "Virtual Cables")
	}
	return filepath.Join(os.TempDir(), "Virtual Cables")
}

func configFilePath() string {
	return filepath.Join(applicationDataDir(), "CONFIG.ini")
}

func loadCableCount() int {
	paths := []string{configFilePath(), "CONFIG.ini"}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "CABLES") {
				n, parseErr := strconv.Atoi(strings.TrimSpace(parts[1]))
				if parseErr == nil && n >= 1 && n <= 32 {
					return n
				}
			}
		}
	}
	return 1
}

func saveCableCount(n int) error {
	dir := applicationDataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("# Virtual Cables configuration\r\nCABLES=%d\r\nLISTEN=127.0.0.1:3240\r\nBUFFER_MS=250\r\n", n)
	return os.WriteFile(configFilePath(), []byte(content), 0o644)
}

func executablePath() string {
	path, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	if clean, err := filepath.Abs(path); err == nil {
		return clean
	}
	return path
}

func newApplicationLogger() (*log.Logger, *os.File, string) {
	candidateDirs := []string{
		applicationDataDir(),
		filepath.Join(filepath.Dir(executablePath()), "Logs"),
		filepath.Join(os.TempDir(), "Virtual Cables"),
	}

	for _, dir := range candidateDirs {
		if strings.TrimSpace(dir) == "" || dir == "." {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		path := filepath.Join(dir, "VirtualCables_USBIP.log")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			continue
		}
		// A Windows GUI process often has no valid stdout handle (for example
		// when started hidden or from Explorer). Put the persistent file first:
		// MultiWriter stops at the first write error, so stdout-first silently
		// prevented every runtime log entry from reaching disk in that case.
		writer := io.MultiWriter(file, os.Stdout)
		return log.New(writer, "", log.Ldate|log.Ltime|log.Lmicroseconds), file, path
	}

	path := filepath.Join(os.TempDir(), "VirtualCables_USBIP.log")
	return log.New(os.Stderr, "", log.Ldate|log.Ltime|log.Lmicroseconds), nil, path
}
