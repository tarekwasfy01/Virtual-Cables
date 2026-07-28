//go:build windows

package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type cableDeviceBroker struct {
	mu        sync.Mutex
	commandMu sync.Mutex
	conn      net.Conn
	desired   int
	starting  bool
}

var deviceBroker cableDeviceBroker

func markBrokerStartFailed() {
	deviceBroker.mu.Lock()
	deviceBroker.starting = false
	deviceBroker.mu.Unlock()
}

func newBrokerToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func startCableBroker() {
	deviceBroker.mu.Lock()
	if deviceBroker.starting || deviceBroker.conn != nil {
		deviceBroker.mu.Unlock()
		return
	}
	deviceBroker.starting = true
	deviceBroker.desired = cableCount
	deviceBroker.mu.Unlock()

	if findUSBIPExecutable() == "" {
		deviceBroker.mu.Lock()
		deviceBroker.starting = false
		deviceBroker.mu.Unlock()
		setStatus(fmt.Sprintf("%d cable(s) configured. Install the driver to add them to Windows Sound.", cableCount))
		return
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		markBrokerStartFailed()
		guiManager.logger.Printf("Could not create administrator broker channel: %v", err)
		return
	}
	token, err := newBrokerToken()
	if err != nil {
		_ = listener.Close()
		markBrokerStartFailed()
		guiManager.logger.Printf("Could not create administrator broker token: %v", err)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		_ = listener.Close()
		markBrokerStartFailed()
		guiManager.logger.Printf("Could not locate executable for administrator broker: %v", err)
		return
	}
	parameters := fmt.Sprintf("--attach-broker --broker-address %s --broker-token %s", listener.Addr().String(), token)
	r, _, callErr := procShellExecute.Call(
		mainWindow,
		uintptr(unsafe.Pointer(utf16("runas"))),
		uintptr(unsafe.Pointer(utf16(exe))),
		uintptr(unsafe.Pointer(utf16(parameters))),
		uintptr(unsafe.Pointer(utf16(filepathDir(exe)))),
		0, // SW_HIDE: the broker is a background process
	)
	if r <= 32 {
		_ = listener.Close()
		deviceBroker.mu.Lock()
		deviceBroker.starting = false
		deviceBroker.mu.Unlock()
		if callErr != nil && callErr != syscall.Errno(0) {
			guiManager.logger.Printf("Administrator broker was not started (%d): %v", r, callErr)
		} else {
			guiManager.logger.Printf("Administrator broker was not started (ShellExecute code %d)", r)
		}
		setStatus(fmt.Sprintf("%d cable(s) configured. Administrator approval was cancelled.", cableCount))
		return
	}
	setStatus(fmt.Sprintf("%d cable(s) configured. Waiting for one-time administrator approval.", cableCount))
	go acceptCableBroker(listener, token)
}

func filepathDir(path string) string {
	if index := strings.LastIndexAny(path, `\/`); index >= 0 {
		return path[:index]
	}
	return "."
}

func acceptCableBroker(listener net.Listener, token string) {
	defer listener.Close()
	if tcp, ok := listener.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Now().Add(60 * time.Second))
	}
	conn, err := listener.Accept()
	if err != nil {
		deviceBroker.mu.Lock()
		deviceBroker.starting = false
		deviceBroker.mu.Unlock()
		guiManager.logger.Printf("Administrator broker did not connect: %v", err)
		return
	}
	reader := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	received, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(received) != token {
		_ = conn.Close()
		deviceBroker.mu.Lock()
		deviceBroker.starting = false
		deviceBroker.mu.Unlock()
		guiManager.logger.Printf("Administrator broker authentication failed")
		return
	}
	_ = conn.SetDeadline(time.Time{})
	deviceBroker.mu.Lock()
	deviceBroker.conn = conn
	deviceBroker.starting = false
	count := deviceBroker.desired
	deviceBroker.mu.Unlock()
	guiManager.logger.Printf("One-time administrator broker connected")
	syncCableDevices(count)
}

func requestCableSync() {
	deviceBroker.mu.Lock()
	deviceBroker.desired = cableCount
	connected := deviceBroker.conn != nil
	starting := deviceBroker.starting
	deviceBroker.mu.Unlock()
	if connected {
		go syncCableDevices(cableCount)
		return
	}
	if !starting {
		startCableBroker()
	}
}

func syncCableDevices(count int) {
	deviceBroker.commandMu.Lock()
	defer deviceBroker.commandMu.Unlock()
	deviceBroker.mu.Lock()
	conn := deviceBroker.conn
	deviceBroker.mu.Unlock()
	if conn == nil {
		return
	}
	_ = conn.SetDeadline(time.Now().Add(45 * time.Second))
	if _, err := fmt.Fprintf(conn, "SYNC %d\n", count); err != nil {
		guiManager.logger.Printf("Could not send device synchronization request: %v", err)
		_ = conn.Close()
		clearBrokerConnection(conn)
		return
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	_ = conn.SetDeadline(time.Time{})
	if err != nil {
		guiManager.logger.Printf("Administrator broker reply failed: %v", err)
		_ = conn.Close()
		clearBrokerConnection(conn)
		return
	}
	reply = strings.TrimSpace(reply)
	if reply != "OK" {
		guiManager.logger.Printf("Windows device synchronization failed: %s", reply)
		setStatus(fmt.Sprintf("%d cable(s) configured. Windows device synchronization failed.", count))
		return
	}
	guiManager.logger.Printf("Windows Sound now synchronized with %d cable(s)", count)
	setStatus(fmt.Sprintf("%d cable(s) configured and synchronized with Windows Sound.", count))
}

func clearBrokerConnection(conn net.Conn) {
	deviceBroker.mu.Lock()
	if deviceBroker.conn == conn {
		deviceBroker.conn = nil
	}
	deviceBroker.mu.Unlock()
}

func stopCableBroker() {
	deviceBroker.mu.Lock()
	defer deviceBroker.mu.Unlock()
	if deviceBroker.conn != nil {
		_, _ = fmt.Fprintln(deviceBroker.conn, "QUIT")
		_ = deviceBroker.conn.Close()
		deviceBroker.conn = nil
	}
}

func runAttachBroker(address, token string, logger *log.Logger) error {
	if !strings.HasPrefix(address, "127.0.0.1:") || len(token) != 64 {
		return fmt.Errorf("invalid broker parameters")
	}
	conn, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connect to main application: %w", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintln(conn, token); err != nil {
		return err
	}
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 1 && fields[0] == "QUIT" {
			return nil
		}
		if len(fields) != 2 || fields[0] != "SYNC" {
			_, _ = fmt.Fprintln(conn, "ERR invalid request")
			continue
		}
		count, parseErr := strconv.Atoi(fields[1])
		if parseErr != nil || count < 1 || count > 32 {
			_, _ = fmt.Fprintln(conn, "ERR invalid cable count")
			continue
		}
		if err := runAttachHelper(count, logger); err != nil {
			logger.Printf("Broker synchronization failed: %v", err)
			_, _ = fmt.Fprintln(conn, "ERR synchronization failed")
			continue
		}
		_, _ = fmt.Fprintln(conn, "OK")
	}
	return scanner.Err()
}
