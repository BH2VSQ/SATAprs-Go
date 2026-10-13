//go:build windows

package pttctl

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

const (
	genericRead   = 0x80000000
	genericWrite  = 0x40000000
	openExisting  = 3
	setRTS        = 3
	clearRTS      = 4
	setDTR        = 5
	clearDTR      = 6
	invalidHandle = ^uintptr(0)
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procCreateFileW    = kernel32.NewProc("CreateFileW")
	procEscapeCommFunc = kernel32.NewProc("EscapeCommFunction")
	procCloseHandle    = kernel32.NewProc("CloseHandle")
)

// Controller keeps the COM handle open while PTT is asserted. This prevents
// the serial driver from releasing RTS/DTR immediately after the line changes.
type Controller struct {
	mu         sync.Mutex
	handle     syscall.Handle
	port       string
	mode       string
	activeHigh bool
}

func New() *Controller { return &Controller{} }

// Set asserts or deasserts RTS/DTR on a dedicated serial port. mode is "rts"
// or "dtr". activeHigh selects whether a high line level means transmit.
func (c *Controller) Set(port, mode string, enabled, activeHigh bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	mode = strings.ToLower(strings.TrimSpace(mode))
	port = strings.TrimSpace(port)
	if mode != "rts" && mode != "dtr" {
		return fmt.Errorf("不支持的串口 PTT 信号 %q", mode)
	}
	if enabled && port == "" {
		return errors.New("请填写串口 PTT 使用的 COM 端口，例如 COM3")
	}

	if enabled {
		portKey := normalizeCOMPath(port)
		if c.handle != 0 && (!strings.EqualFold(c.port, portKey) || c.mode != mode || c.activeHigh != activeHigh) {
			_ = c.releaseLocked()
		}
		if c.handle == 0 {
			h, normalizedPort, err := openCOM(port)
			if err != nil {
				return err
			}
			c.handle, c.port, c.mode, c.activeHigh = h, normalizedPort, mode, activeHigh
			// Put both control lines into a known inactive electrical state first.
			_ = callEscape(c.handle, clearRTS)
			_ = callEscape(c.handle, clearDTR)
		}
		return setSignal(c.handle, c.mode, true, c.activeHigh)
	}
	if c.handle == 0 {
		return nil
	}
	return c.releaseLocked()
}

func (c *Controller) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.releaseLocked()
}

func (c *Controller) releaseLocked() error {
	if c.handle == 0 {
		return nil
	}
	lineErr := setSignal(c.handle, c.mode, false, c.activeHigh)
	closeErr := closeHandle(c.handle)
	c.handle = 0
	c.port = ""
	c.mode = ""
	c.activeHigh = false
	if lineErr != nil {
		return lineErr
	}
	return closeErr
}

func normalizeCOMPath(port string) string {
	name := strings.TrimSpace(port)
	if !strings.HasPrefix(strings.ToLower(name), `\\.\`) {
		name = `\\.\` + name
	}
	return name
}

func openCOM(port string) (syscall.Handle, string, error) {
	name := normalizeCOMPath(port)
	ptr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, "", fmt.Errorf("无效的 COM 端口: %w", err)
	}
	r1, _, callErr := procCreateFileW.Call(
		uintptr(unsafePointer(ptr)),
		genericRead|genericWrite,
		0,
		0,
		openExisting,
		0,
		0,
	)
	if r1 == invalidHandle {
		if callErr == syscall.Errno(0) {
			callErr = errors.New("CreateFileW 返回无效句柄")
		}
		return 0, "", fmt.Errorf("打开串口 %s 失败: %w", port, callErr)
	}
	return syscall.Handle(r1), name, nil
}

// unsafePointer keeps the syscall boundary explicit and avoids exposing unsafe
// operations to the rest of the package.
func unsafePointer(p *uint16) unsafe.Pointer { return unsafe.Pointer(p) }

func setSignal(handle syscall.Handle, mode string, enabled, activeHigh bool) error {
	high := enabled == activeHigh
	command := clearRTS
	if mode == "rts" {
		if high {
			command = setRTS
		}
	} else if mode == "dtr" {
		command = clearDTR
		if high {
			command = setDTR
		}
	} else {
		return fmt.Errorf("无效的 PTT 信号 %q", mode)
	}
	return callEscape(handle, uintptr(command))
}

func callEscape(handle syscall.Handle, command uintptr) error {
	r1, _, callErr := procEscapeCommFunc.Call(uintptr(handle), command)
	if r1 == 0 {
		if callErr == syscall.Errno(0) {
			callErr = errors.New("EscapeCommFunction 调用失败")
		}
		return fmt.Errorf("设置串口 RTS/DTR 电平失败: %w", callErr)
	}
	return nil
}

func closeHandle(handle syscall.Handle) error {
	r1, _, callErr := procCloseHandle.Call(uintptr(handle))
	if r1 == 0 {
		if callErr == syscall.Errno(0) {
			callErr = errors.New("CloseHandle 调用失败")
		}
		return fmt.Errorf("关闭串口句柄失败: %w", callErr)
	}
	return nil
}
