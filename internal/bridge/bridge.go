package bridge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/TIANLI0/THRM/internal/appmeta"
	"github.com/TIANLI0/THRM/internal/types"
	"golang.org/x/sys/windows"
)

type Manager struct {
	cmd          *exec.Cmd
	conn         net.Conn
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	stdoutReader *bufio.Reader
	pipeName     string
	transport    string
	ownsCmd      bool
	state        string
	lastError    string
	mutex        sync.Mutex
	logger       types.Logger
}

type stdioConn struct {
	reader *bufio.Reader
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

type stdioAddr string

func newStdioConn(stdin io.WriteCloser, stdout io.ReadCloser, reader *bufio.Reader) net.Conn {
	return &stdioConn{
		reader: reader,
		stdin:  stdin,
		stdout: stdout,
	}
}

func (c *stdioConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *stdioConn) Write(p []byte) (int, error) {
	return c.stdin.Write(p)
}

func (c *stdioConn) Close() error {
	var closeErr error
	if c.stdin != nil {
		closeErr = c.stdin.Close()
		c.stdin = nil
	}
	if c.stdout != nil {
		if err := c.stdout.Close(); closeErr == nil {
			closeErr = err
		}
		c.stdout = nil
	}
	return closeErr
}

func (c *stdioConn) LocalAddr() net.Addr {
	return stdioAddr("stdio-local")
}

func (c *stdioConn) RemoteAddr() net.Addr {
	return stdioAddr("stdio-remote")
}

func (c *stdioConn) SetDeadline(time.Time) error {
	return nil
}

func (c *stdioConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *stdioConn) SetWriteDeadline(time.Time) error {
	return nil
}

func (a stdioAddr) Network() string {
	return "stdio"
}

func (a stdioAddr) String() string {
	return string(a)
}

const (
	bridgeDefaultCommandTimeout = 3 * time.Second
	bridgeGetTemperatureTimeout = 10 * time.Second
	bridgeRestartPawnIOTimeout  = 20 * time.Second
	bridgeExitTimeout           = 2 * time.Second
	bridgeProcessExitWait       = 8 * time.Second
	bridgeStartupTimeout        = 5 * time.Second
	windowsStillActive          = 259

	BridgeStateNotStarted = "not_started"
	BridgeStateStarting   = "starting"
	BridgeStateRunning    = "running_owned"
	BridgeStateAttached   = "attached"
	BridgeStateDegraded   = "degraded"
	BridgeStateStopping   = "stopping"
	BridgeStateStopped    = "stopped"
	BridgeStateFailed     = "failed"
)

func NewManager(logger types.Logger) *Manager {
	return &Manager{
		logger: logger,
		state:  BridgeStateNotStarted,
	}
}

func (m *Manager) setState(state string, err error) {
	m.state = state
	if err != nil {
		m.lastError = err.Error()
		return
	}

	switch state {
	case BridgeStateRunning, BridgeStateAttached, BridgeStateStopped, BridgeStateNotStarted:
		m.lastError = ""
	}
}

func (m *Manager) EnsureRunning() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.stdin != nil && m.stdoutReader != nil {
		if isProcessRunning(m.cmd) {
			m.setState(BridgeStateRunning, nil)
			return nil
		}

		err := fmt.Errorf("bridge process exited unexpectedly")
		m.setState(BridgeStateDegraded, err)
		m.closeConnUnsafe()
		m.releaseOwnedProcessUnsafe()
		m.pipeName = ""
	}

	return m.startStdio()
}

func (m *Manager) startStdio() error {
	m.setState(BridgeStateStarting, nil)

	exeDir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		m.setState(BridgeStateFailed, err)
		return fmt.Errorf("鑾峰彇绋嬪簭鐩綍澶辫触: %v", err)
	}

	possiblePaths := appmeta.BridgeExecutableCandidates(exeDir)
	bridgePath := appmeta.FirstExistingPath(possiblePaths)
	if bridgePath == "" {
		err := fmt.Errorf("%s 涓嶅瓨鍦紝宸插皾璇曚互涓嬭矾寰? %v", appmeta.BridgeExecutableName, possiblePaths)
		m.setState(BridgeStateFailed, err)
		return err
	}

	m.logger.Info("鎵惧埌妗ยู帴绋嬪簭: %s", bridgePath)

	cmd := exec.Command(bridgePath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		m.setState(BridgeStateFailed, err)
		return fmt.Errorf("鍒涘缓 stdin 绠￠亾澶辫触: %v", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.setState(BridgeStateFailed, err)
		return fmt.Errorf("鍒涘缓 stdout 绠￠亾澶辫触: %v", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		m.setState(BridgeStateFailed, err)
		return fmt.Errorf("鍒涘缓 stderr 绠￠亾澶辫触: %v", err)
	}

	if err := cmd.Start(); err != nil {
		m.setState(BridgeStateFailed, err)
		return fmt.Errorf("鍚姩妗ยู帴绋嬪簭澶辫触: %v", err)
	}

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				m.logger.Error("妗ยู帴绋嬪簭 stderr: %s", line)
			}
		}
		if err := scanner.Err(); err != nil {
			m.logger.Debug("璇诲彇妗ยู帴绋嬪簭 stderr 澶辫触: %v", err)
		}
	}()

	stdoutReader := bufio.NewReader(stdout)
	if err := m.waitForReady(stdoutReader, bridgeStartupTimeout); err != nil {
		_ = cmd.Process.Kill()
		m.setState(BridgeStateFailed, err)
		return err
	}

	m.cmd = cmd
	m.stdin = stdin
	m.stdout = stdout
	m.stdoutReader = stdoutReader
	m.conn = newStdioConn(stdin, stdout, stdoutReader)
	m.pipeName = ""
	m.transport = "stdio"
	m.ownsCmd = true
	m.setState(BridgeStateRunning, nil)
	m.logger.Info("妗ยู帴绋嬪簭鍚姩鎴愬姛锛岄€氫俊鏂瑰紡: stdio")
	return nil
}

func (m *Manager) waitForReady(reader *bufio.Reader, timeout time.Duration) error {
	readyCh := make(chan error, 1)
	go func() {
		line, err := reader.ReadString('\n')
		if err != nil {
			readyCh <- fmt.Errorf("read bridge startup handshake failed: %v", err)
			return
		}

		line = strings.TrimSpace(line)
		switch {
		case strings.EqualFold(line, "READY:STDIO"):
			readyCh <- nil
		case strings.HasPrefix(line, "ERROR:"):
			readyCh <- fmt.Errorf("bridge startup failed: %s", strings.TrimSpace(strings.TrimPrefix(line, "ERROR:")))
		case line == "":
			readyCh <- fmt.Errorf("bridge did not return a startup handshake")
		default:
			readyCh <- fmt.Errorf("bridge returned an unexpected startup line: %s", line)
		}
	}()

	select {
	case err := <-readyCh:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("waiting for bridge startup timed out")
	}
}

func bridgeCommandTimeoutFor(cmdType string) time.Duration {
	switch cmdType {
	case "GetTemperature":
		return bridgeGetTemperatureTimeout
	case "RestartPawnIO":
		return bridgeRestartPawnIOTimeout
	case "Exit":
		return bridgeExitTimeout
	default:
		return bridgeDefaultCommandTimeout
	}
}

func (m *Manager) SendCommand(cmdType, data string) (*types.BridgeResponse, error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	return m.sendCommandUnsafe(cmdType, data)
}

func (m *Manager) sendCommandUnsafe(cmdType, data string) (*types.BridgeResponse, error) {
	if m.conn == nil {
		return nil, fmt.Errorf("桥接程序未连接")
	}

	conn := m.conn
	timeout := bridgeCommandTimeoutFor(cmdType)

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		m.logger.Debug("设置桥接命令超时失败: %v", err)
	}
	defer func() {
		_ = conn.SetDeadline(time.Time{})
	}()

	cmdBytes, err := json.Marshal(types.BridgeCommand{
		Type: cmdType,
		Data: data,
	})
	if err != nil {
		return nil, fmt.Errorf("序列化命令失败: %v", err)
	}

	if _, err := conn.Write(append(cmdBytes, '\n')); err != nil {
		m.setState(BridgeStateDegraded, err)
		m.closeConnUnsafe()
		return nil, fmt.Errorf("发送 %s 命令失败 (timeout=%s): %v", cmdType, timeout, err)
	}

	responseBytes, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		m.setState(BridgeStateDegraded, err)
		m.closeConnUnsafe()
		return nil, fmt.Errorf("读取 %s 响应失败 (timeout=%s): %v", cmdType, timeout, err)
	}

	var response types.BridgeResponse
	if err := json.Unmarshal(responseBytes, &response); err != nil {
		m.setState(BridgeStateDegraded, err)
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	if m.ownsCmd {
		m.setState(BridgeStateRunning, nil)
	} else {
		m.setState(BridgeStateAttached, nil)
	}

	return &response, nil
}

func (m *Manager) closeConnUnsafe() {
	if m.conn != nil {
		_ = m.conn.Close()
		m.conn = nil
	}
	if m.stdin != nil {
		_ = m.stdin.Close()
		m.stdin = nil
	}
	if m.stdout != nil {
		_ = m.stdout.Close()
		m.stdout = nil
	}
	m.stdoutReader = nil
	m.transport = ""
}

func (m *Manager) releaseOwnedProcessUnsafe() {
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Release()
	}
	m.cmd = nil
	m.ownsCmd = false
}

func isProcessRunning(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}

	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}

	return exitCode == windowsStillActive
}

func (m *Manager) Stop() {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.stopUnsafe()
}

func (m *Manager) stopUnsafe() {
	m.setState(BridgeStateStopping, nil)

	ownedCmd := m.cmd
	ownsCmd := m.ownsCmd

	m.cmd = nil
	m.ownsCmd = false
	m.pipeName = ""

	if m.conn != nil {
		if ownsCmd {
			_, _ = m.sendCommandUnsafe("Exit", "")
		}
		m.closeConnUnsafe()
	}

	if ownsCmd && ownedCmd != nil && ownedCmd.Process != nil {
		done := make(chan error, 1)
		go func() {
			done <- ownedCmd.Wait()
		}()

		select {
		case <-done:
		case <-time.After(bridgeProcessExitWait):
			_ = ownedCmd.Process.Kill()
		}
	}

	m.setState(BridgeStateStopped, nil)
}

func (m *Manager) GetTemperature(selection types.TemperatureSelection) types.BridgeTemperatureData {
	selection = types.NormalizeTemperatureSelection(selection)
	selectionPayload, err := json.Marshal(selection)
	if err != nil {
		return types.BridgeTemperatureData{
			Success: false,
			Error:   fmt.Sprintf("序列化温度选择配置失败: %v", err),
		}
	}

	if err := m.EnsureRunning(); err != nil {
		return types.BridgeTemperatureData{
			Success: false,
			Error:   fmt.Sprintf("启动桥接程序失败: %v", err),
		}
	}

	response, err := m.SendCommand("GetTemperature", string(selectionPayload))
	if err != nil {
		return types.BridgeTemperatureData{
			Success: false,
			Error:   fmt.Sprintf("桥接程序通信失败: %v", err),
		}
	}

	if !response.Success {
		if response.Data != nil {
			result := *response.Data
			result.Success = false
			if strings.TrimSpace(response.Error) != "" {
				result.Error = response.Error
			}
			return result
		}
		return types.BridgeTemperatureData{
			Success: false,
			Error:   response.Error,
		}
	}

	if response.Data == nil {
		return types.BridgeTemperatureData{
			Success: false,
			Error:   "桥接程序返回空数据",
		}
	}

	return *response.Data
}

func (m *Manager) GetStatus() map[string]any {
	m.mutex.Lock()
	state := m.state
	ownsCmd := m.ownsCmd
	pipeName := m.pipeName
	transport := m.transport
	lastError := m.lastError
	m.mutex.Unlock()

	exeDir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		return map[string]any{
			"exists": false,
			"error":  fmt.Sprintf("获取程序目录失败: %v", err),
			"state":  state,
		}
	}

	possiblePaths := appmeta.BridgeExecutableCandidates(exeDir)
	bridgePath := appmeta.FirstExistingPath(possiblePaths)
	if bridgePath == "" {
		return map[string]any{
			"exists":      false,
			"state":       state,
			"ownsProcess": ownsCmd,
			"pipeName":    pipeName,
			"transport":   transport,
			"lastError":   lastError,
			"triedPaths":  possiblePaths,
			"error":       fmt.Sprintf("%s 不存在", appmeta.BridgeExecutableName),
		}
	}

	testResult := m.GetTemperature(types.GetDefaultTemperatureSelection())

	m.mutex.Lock()
	state = m.state
	ownsCmd = m.ownsCmd
	pipeName = m.pipeName
	transport = m.transport
	lastError = m.lastError
	m.mutex.Unlock()

	return map[string]any{
		"exists":      true,
		"path":        bridgePath,
		"working":     testResult.Success,
		"state":       state,
		"ownsProcess": ownsCmd,
		"pipeName":    pipeName,
		"transport":   transport,
		"lastError":   lastError,
		"testData":    testResult,
	}
}

func (m *Manager) RestartPawnIO() (types.BridgeTemperatureData, error) {
	if err := m.EnsureRunning(); err != nil {
		return types.BridgeTemperatureData{
			Success: false,
			Error:   fmt.Sprintf("启动桥接程序失败: %v", err),
		}, err
	}

	m.logger.Info("正在通过桥接程序重启 PawnIO 驱动...")
	response, err := m.SendCommand("RestartPawnIO", "")
	if err != nil {
		return types.BridgeTemperatureData{
			Success: false,
			Error:   fmt.Sprintf("发送 RestartPawnIO 命令失败: %v", err),
		}, err
	}

	if !response.Success {
		return types.BridgeTemperatureData{
			Success: false,
			Error:   response.Error,
		}, fmt.Errorf("RestartPawnIO 失败: %s", response.Error)
	}

	result := types.BridgeTemperatureData{Success: true}
	if response.Data != nil {
		result = *response.Data
	}

	m.logger.Info("PawnIO 驱动重启成功")
	return result, nil
}
