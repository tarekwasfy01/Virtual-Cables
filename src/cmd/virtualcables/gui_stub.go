//go:build !windows

package main

import (
	"fmt"
	"os"
)

func runGUI(manager *appServer)      { select {} }
func showFatalDialog(message string) { fmt.Fprintln(os.Stderr, message) }
