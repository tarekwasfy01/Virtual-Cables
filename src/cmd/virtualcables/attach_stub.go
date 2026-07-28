//go:build !windows

package main

import (
	"fmt"
	"log"
)

func runAttachHelper(count int, logger *log.Logger) error {
	return fmt.Errorf("USB/IP attachment is only supported on Windows")
}

func runAttachBroker(address, token string, logger *log.Logger) error {
	return fmt.Errorf("USB/IP attachment is only supported on Windows")
}
