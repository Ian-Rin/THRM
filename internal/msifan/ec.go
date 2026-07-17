package msifan

import (
	"fmt"
	"sync"
	"time"
)

// PortIO 原始 x86 IO 端口访问。Windows 上由 WinRing0 驱动实现；
// 测试中用模拟 EC 状态机实现。
type PortIO interface {
	ReadPort(port uint16) (byte, error)
	WritePort(port uint16, value byte) error
}

// 标准 ACPI EC 接口（ACPI spec ch. 12）。
const (
	ecPortCmd  = 0x66 // EC_SC：写命令 / 读状态
	ecPortData = 0x62 // EC_DATA

	ecCmdRead  = 0x80 // RD_EC
	ecCmdWrite = 0x81 // WR_EC

	ecStatusOBF = 0x01 // 输出缓冲满：可从 0x62 读
	ecStatusIBF = 0x02 // 输入缓冲满：EC 尚未取走上一字节
)

// EC 对单个 ACPI EC 的串行化读写访问。
type EC struct {
	mu      sync.Mutex
	io      PortIO
	timeout time.Duration // 单次状态等待上限
	retries int
}

// NewEC 创建 EC 访问器。
func NewEC(io PortIO) *EC {
	return &EC{io: io, timeout: 25 * time.Millisecond, retries: 3}
}

// ReadReg 读取 EC RAM 中 reg 处的一个字节。
func (e *EC) ReadReg(reg byte) (byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var lastErr error
	for i := 0; i < e.retries; i++ {
		v, err := e.readByteLocked(reg)
		if err == nil {
			return v, nil
		}
		lastErr = err
	}
	return 0, lastErr
}

// WriteReg 向 EC RAM 中 reg 处写入一个字节。
func (e *EC) WriteReg(reg, value byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var lastErr error
	for i := 0; i < e.retries; i++ {
		if err := e.writeByteLocked(reg, value); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return lastErr
}

// ReadWordBE 读取 reg（高位）与 reg+1（低位）组成的 16 位大端值。
func (e *EC) ReadWordBE(reg byte) (uint16, error) {
	hi, err := e.ReadReg(reg)
	if err != nil {
		return 0, err
	}
	lo, err := e.ReadReg(reg + 1)
	if err != nil {
		return 0, err
	}
	return uint16(hi)<<8 | uint16(lo), nil
}

// ReadString 从 reg 起连续读取 n 字节 ASCII。
func (e *EC) ReadString(reg byte, n int) (string, error) {
	buf := make([]byte, n)
	for i := 0; i < n; i++ {
		b, err := e.ReadReg(reg + byte(i))
		if err != nil {
			return "", err
		}
		buf[i] = b
	}
	return string(buf), nil
}

func (e *EC) readByteLocked(reg byte) (byte, error) {
	if err := e.waitIBFClear(); err != nil {
		return 0, err
	}
	if err := e.io.WritePort(ecPortCmd, ecCmdRead); err != nil {
		return 0, err
	}
	if err := e.waitIBFClear(); err != nil {
		return 0, err
	}
	if err := e.io.WritePort(ecPortData, reg); err != nil {
		return 0, err
	}
	if err := e.waitIBFClear(); err != nil {
		return 0, err
	}
	if err := e.waitOBFSet(); err != nil {
		return 0, err
	}
	return e.io.ReadPort(ecPortData)
}

func (e *EC) writeByteLocked(reg, value byte) error {
	if err := e.waitIBFClear(); err != nil {
		return err
	}
	if err := e.io.WritePort(ecPortCmd, ecCmdWrite); err != nil {
		return err
	}
	if err := e.waitIBFClear(); err != nil {
		return err
	}
	if err := e.io.WritePort(ecPortData, reg); err != nil {
		return err
	}
	if err := e.waitIBFClear(); err != nil {
		return err
	}
	return e.io.WritePort(ecPortData, value)
}

func (e *EC) waitIBFClear() error {
	return e.waitStatus(ecStatusIBF, false)
}

func (e *EC) waitOBFSet() error {
	return e.waitStatus(ecStatusOBF, true)
}

func (e *EC) waitStatus(mask byte, set bool) error {
	deadline := time.Now().Add(e.timeout)
	for {
		st, err := e.io.ReadPort(ecPortCmd)
		if err != nil {
			return err
		}
		if (st&mask != 0) == set {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("msifan: EC 状态等待超时 (mask=0x%02X set=%v status=0x%02X)", mask, set, st)
		}
		time.Sleep(20 * time.Microsecond)
	}
}
