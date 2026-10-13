//go:build !windows

package pttctl

import "errors"

// Controller is a build-safe placeholder for non-Windows test environments.
type Controller struct{}

func New() *Controller { return &Controller{} }

func (c *Controller) Set(port, mode string, enabled, activeHigh bool) error {
	return errors.New("RTS/DTR 串口 PTT 控制目前仅支持 Windows 桌面版")
}

func (c *Controller) Close() error { return nil }
