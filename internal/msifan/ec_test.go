package msifan

import (
	"testing"
)

// mockEC 模拟一个标准 ACPI EC 的端口级状态机（0x66/0x62）。
type mockEC struct {
	ram [256]byte

	status  byte
	pending byte // 已收到的命令（0x80/0x81）
	stage   int  // 0=等命令 1=等地址 2=等数据(写)/数据就绪(读)
	addr    byte
	out     byte

	reads, writes int
}

func (m *mockEC) ReadPort(port uint16) (byte, error) {
	switch port {
	case ecPortCmd:
		return m.status, nil
	case ecPortData:
		m.status &^= ecStatusOBF
		return m.out, nil
	}
	return 0, nil
}

func (m *mockEC) WritePort(port uint16, value byte) error {
	switch port {
	case ecPortCmd:
		m.pending = value
		m.stage = 1
	case ecPortData:
		switch m.stage {
		case 1:
			m.addr = value
			if m.pending == ecCmdRead {
				m.out = m.ram[m.addr]
				m.status |= ecStatusOBF
				m.reads++
				m.stage = 0
			} else {
				m.stage = 2
			}
		case 2:
			m.ram[m.addr] = value
			m.writes++
			m.stage = 0
		}
	}
	return nil
}

func TestECReadWriteByte(t *testing.T) {
	mock := &mockEC{}
	mock.ram[0x68] = 67 // CPU 温度
	ec := NewEC(mock)

	v, err := ec.ReadReg(0x68)
	if err != nil || v != 67 {
		t.Fatalf("ReadReg(0x68) = %d, %v; want 67", v, err)
	}

	if err := ec.WriteReg(0x72, 55); err != nil {
		t.Fatalf("WriteReg: %v", err)
	}
	if mock.ram[0x72] != 55 {
		t.Fatalf("ram[0x72] = %d, want 55", mock.ram[0x72])
	}
}

func TestECReadWordBE(t *testing.T) {
	mock := &mockEC{}
	// RPM 原始值 0x00D2 = 210 → 478000/210 ≈ 2276 RPM
	mock.ram[0xC8] = 0x00
	mock.ram[0xC9] = 0xD2
	ec := NewEC(mock)

	raw, err := ec.ReadWordBE(0xC8)
	if err != nil || raw != 0x00D2 {
		t.Fatalf("ReadWordBE = 0x%04X, %v; want 0x00D2", raw, err)
	}
}

func TestECReadString(t *testing.T) {
	mock := &mockEC{}
	copy(mock.ram[0xA0:], "15M1IMS2.111")
	ec := NewEC(mock)

	s, err := ec.ReadString(0xA0, 12)
	if err != nil || s != "15M1IMS2.111" {
		t.Fatalf("ReadString = %q, %v", s, err)
	}
}

// 卡死的 EC（IBF 永远置位）必须超时报错而不是挂死。
type stuckEC struct{}

func (stuckEC) ReadPort(port uint16) (byte, error)     { return ecStatusIBF, nil }
func (stuckEC) WritePort(port uint16, val byte) error  { return nil }

func TestECTimeout(t *testing.T) {
	ec := NewEC(stuckEC{})
	ec.timeout = 2 // 极短超时（纳秒级），立即超时
	ec.retries = 1
	if _, err := ec.ReadReg(0x68); err == nil {
		t.Fatal("stuck EC must return timeout error")
	}
}
