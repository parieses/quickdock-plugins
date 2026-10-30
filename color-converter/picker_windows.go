//go:build windows

package main

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32                 = syscall.NewLazyDLL("user32.dll")
	gdi32                  = syscall.NewLazyDLL("gdi32.dll")
	procSetProcessDPIAware = user32.NewProc("SetProcessDPIAware")
	procGetCursorPos       = user32.NewProc("GetCursorPos")
	procGetDC              = user32.NewProc("GetDC")
	procReleaseDC          = user32.NewProc("ReleaseDC")
	procGetPixel           = gdi32.NewProc("GetPixel")
	procGetAsyncKeyState   = user32.NewProc("GetAsyncKeyState")
)

type point struct {
	X, Y int32
}

const (
	vkF8     = 0x77
	vkEscape = 0x1B
)

// pickScreenColor 采样鼠标光标当前位置的像素颜色。
// SetProcessDPIAware 确保高 DPI/多显示器缩放场景下 GetCursorPos 的物理坐标
// 与 GetPixel 的屏幕设备坐标一致，避免取样偏移。
// 注意：LazyProc.Call 返回的 err 恒非 nil（syscall.Errno(0) 装进 error 接口的坑），
// 只能以返回值判断成败，绝不能 if err != nil。
func pickScreenColor() (r, g, b uint8, err error) {
	procSetProcessDPIAware.Call()

	var pt point
	ret, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	if ret == 0 {
		return 0, 0, 0, fmt.Errorf("获取鼠标位置失败")
	}

	hdc, _, _ := procGetDC.Call(0) // 0 = 整个屏幕
	if hdc == 0 {
		return 0, 0, 0, fmt.Errorf("获取屏幕 DC 失败")
	}
	defer procReleaseDC.Call(0, hdc)

	cr, _, _ := procGetPixel.Call(hdc, uintptr(uint32(pt.X)), uintptr(uint32(pt.Y)))
	// 坐标经 uint32 中转：多显示器时 X/Y 可能为负，直接 uintptr(int32) 会得到
	// 符号扩展后的巨大值；uint32 保留低 32 位补码模式，与 API 的 int 参数位型一致
	if cr == 0xFFFFFFFF {
		return 0, 0, 0, fmt.Errorf("读取像素失败（坐标可能不在屏幕内）")
	}
	// COLORREF 布局为 0x00BBGGRR
	return uint8(cr & 0xFF), uint8((cr >> 8) & 0xFF), uint8((cr >> 16) & 0xFF), nil
}

// isKeyDown 查询虚拟键当前物理按下状态（GetAsyncKeyState 高位）
func isKeyDown(vk uint32) bool {
	ret, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
	return ret&0x8000 != 0
}

// waitPickLoop 热键等待模式：边沿检测 F8 取色 / ESC 取消，60s 超时。
// 边沿检测（上轮未按 + 本轮按下才触发）避免按住不放重复取色；
// epoch 与全局代数不符时静默退出（被新一轮 start 取代）。
func waitPickLoop(epoch int) {
	defer func() {
		if r := recover(); r != nil {
			qdLogPanic("color-converter waitPickLoop panic", r)
		}
	}()
	const waitTimeout = 60 * time.Second
	deadline := time.Now().Add(waitTimeout)
	var f8WasDown, escWasDown bool

	for time.Now().Before(deadline) {
		pickMu.Lock()
		stale := epoch != pickEpoch || pickState.Status != "running"
		pickMu.Unlock()
		if stale {
			return
		}

		if isKeyDown(vkF8) && !f8WasDown {
			r, g, b, err := pickScreenColor()
			if err != nil {
				markPickErr(epoch, err.Error())
				return
			}
			// 复制交由前端经宿主桥接完成：后端 host.clipboard.write 是 fire-and-forget 回调，
			// 无法确认是否真的写入系统剪贴板（实测此路径在取色场景下不可靠，且 copied 标志会
			// 被谎报为 true）。前端 plugin:copy 桥接已在本插件「复制」按钮上验证可用，并会
			// 据真实回包反馈「已复制 / 复制失败」。故后端只负责采样颜色，copied 置 false。
			markPick(epoch, "done", r, g, b, false)
			return
		}
		f8WasDown = isKeyDown(vkF8)

		if isKeyDown(vkEscape) && !escWasDown {
			markPick(epoch, "cancelled", 0, 0, 0, false)
			return
		}
		escWasDown = isKeyDown(vkEscape)

		time.Sleep(30 * time.Millisecond)
	}
	markPick(epoch, "cancelled", 0, 0, 0, false) // 超时视为取消
}
