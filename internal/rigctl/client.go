package rigctl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client controls a running Hamlib rigctld daemon over its text TCP protocol.
// The connection is opened per command, allowing this app to coexist with a
// radio-control program when the rigctld backend supports shared access.
type Client struct {
	Address string
	Timeout time.Duration
}

func New(address string) *Client {
	return &Client{Address: address, Timeout: 2 * time.Second}
}

// PTTSession keeps the rigctld TCP connection alive for the full duration of a
// keyed transmission. Some radio/rigctld backends de-key when the control socket
// closes, so PTT-on and PTT-off must be sent on the same connection.
type PTTSession struct {
	mu      sync.Mutex
	conn    net.Conn
	reader  *bufio.Reader
	timeout time.Duration
	closed  bool
}

// StartPTTSession sends a Hamlib PTT value and leaves the control connection
// open until SetPTT(ctx, 0) and Close are called.
func (c *Client) StartPTTSession(ctx context.Context, value int) (*PTTSession, string, error) {
	if value < 1 || value > 3 {
		return nil, "", fmt.Errorf("PTT 启动命令值必须在 1 到 3 之间")
	}
	if strings.TrimSpace(c.Address) == "" {
		return nil, "", errors.New("Hamlib rigctld 地址为空")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.Address)
	if err != nil {
		return nil, "", fmt.Errorf("连接 rigctld %s 失败: %w", c.Address, err)
	}
	session := &PTTSession{conn: conn, reader: bufio.NewReader(conn), timeout: timeout}
	response, err := session.command(ctx, fmt.Sprintf("T %d\n", value))
	if err != nil {
		_ = session.Close()
		return nil, response, err
	}
	return session, response, nil
}

// SetPTT sends a command over the existing session. The socket remains open
// after PTT-on; after PTT-off, the caller should Close the session.
func (s *PTTSession) SetPTT(ctx context.Context, value int) (string, error) {
	if value < 0 || value > 3 {
		return "", fmt.Errorf("PTT 命令值必须在 0 到 3 之间")
	}
	return s.command(ctx, fmt.Sprintf("T %d\n", value))
}

func (s *PTTSession) command(ctx context.Context, command string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.conn == nil {
		return "", errors.New("rigctld PTT 会话已关闭")
	}
	deadline := time.Now().Add(s.timeout)
	if ctx != nil {
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
	}
	if err := s.conn.SetDeadline(deadline); err != nil {
		return "", fmt.Errorf("设置 rigctld 会话期限失败: %w", err)
	}
	if _, err := s.conn.Write([]byte(command)); err != nil {
		_ = s.closeLocked()
		return "", fmt.Errorf("发送 rigctld 命令失败: %w", err)
	}
	line, err := s.reader.ReadString('\n')
	if err != nil {
		_ = s.closeLocked()
		return "", fmt.Errorf("读取 rigctld 响应失败: %w", err)
	}
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "RPRT ") {
		code, parseErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "RPRT ")))
		if parseErr != nil {
			return line, fmt.Errorf("无法解析 rigctld 响应: %s", line)
		}
		if code != 0 {
			return line, fmt.Errorf("rigctld 返回错误代码 %d", code)
		}
	}
	return line, nil
}

func (s *PTTSession) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeLocked()
}

func (s *PTTSession) closeLocked() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.conn != nil {
		err := s.conn.Close()
		s.conn = nil
		return err
	}
	return nil
}

func (c *Client) SetPTT(ctx context.Context, enabled bool) error {
	_, err := c.SetPTTResponse(ctx, enabled)
	return err
}

// SetPTTResponse sets PTT and returns the raw rigctld acknowledgement (usually
// "RPRT 0") so the UI can show evidence from the actual radio-control link.
func (c *Client) SetPTTResponse(ctx context.Context, enabled bool) (string, error) {
	value := 0
	if enabled {
		value = 1
	}
	return c.SetPTTValueResponse(ctx, value)
}

// SetPTTValueResponse supports Hamlib's PTT values: 0=OFF, 1=ON,
// 2=ON_MIC and 3=ON_DATA. This lets radio/audio setups choose the proper keying path.
func (c *Client) SetPTTValueResponse(ctx context.Context, value int) (string, error) {
	if value < 0 || value > 3 {
		return "", fmt.Errorf("PTT 命令值必须在 0 到 3 之间")
	}
	return c.commandResponse(ctx, fmt.Sprintf("T %d\n", value))
}

func (c *Client) GetPTT(ctx context.Context) (int, error) {
	line, err := c.commandResponse(ctx, "t\n")
	if err != nil {
		return 0, err
	}
	valueText := strings.TrimSpace(line)
	if i := strings.LastIndex(valueText, ":"); i >= 0 {
		valueText = strings.TrimSpace(valueText[i+1:])
	}
	fields := strings.Fields(valueText)
	if len(fields) > 0 {
		valueText = fields[len(fields)-1]
	}
	value, err := strconv.Atoi(valueText)
	if err != nil {
		return 0, fmt.Errorf("无法解析 rigctld PTT 状态 %q", line)
	}
	if value < 0 || value > 3 {
		return 0, fmt.Errorf("rigctld 返回未知 PTT 状态 %d", value)
	}
	return value, nil
}

func (c *Client) Test(ctx context.Context) error {
	_, err := c.TestResponse(ctx)
	return err
}

// TestResponse issues the safe PTT-off command and returns rigctld's raw
// acknowledgement. This tests both TCP connectivity and command handling.
func (c *Client) TestResponse(ctx context.Context) (string, error) {
	return c.commandResponse(ctx, "T 0\n")
}

func (c *Client) command(ctx context.Context, command string) error {
	_, err := c.commandResponse(ctx, command)
	return err
}

func (c *Client) commandResponse(ctx context.Context, command string) (string, error) {
	if strings.TrimSpace(c.Address) == "" {
		return "", errors.New("Hamlib rigctld 地址为空")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.Address)
	if err != nil {
		return "", fmt.Errorf("连接 rigctld %s 失败: %w", c.Address, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte(command)); err != nil {
		return "", fmt.Errorf("发送 rigctld 命令失败: %w", err)
	}
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("读取 rigctld 响应失败: %w", err)
	}
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "RPRT ") {
		code, parseErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "RPRT ")))
		if parseErr != nil {
			return "", fmt.Errorf("无法解析 rigctld 响应: %s", line)
		}
		if code != 0 {
			return "", fmt.Errorf("rigctld 返回错误代码 %d", code)
		}
		return line, nil
	}
	return line, nil
}
