//go:build !windows

package main

import (
	"errors"
	"time"
)

func pickScreenColor() (r, g, b uint8, err error) {
	return 0, 0, 0, errors.New("屏幕取色目前仅支持 Windows")
}

func waitPickLoop(epoch int) {
	time.Sleep(50 * time.Millisecond)
	markPick(epoch, "cancelled", 0, 0, 0, false)
}
