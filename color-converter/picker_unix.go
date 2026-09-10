//go:build darwin || linux

package main

import (
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"time"
)

func pickScreenColor() (r, g, b uint8, err error) {
	return 0, 0, 0, errors.New("屏幕取色目前仅支持 Windows")
}

func waitPickLoop(epoch int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "color-converter waitPickLoop panic: %v\n%s\n", r, debug.Stack())
		}
	}()
	time.Sleep(50 * time.Millisecond)
	markPick(epoch, "cancelled", 0, 0, 0, false)
}
