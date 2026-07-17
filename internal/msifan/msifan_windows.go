//go:build windows

package msifan

import (
	"fmt"
	"strings"
	"sync"

	"github.com/TIANLI0/THRM/internal/types"
)

type winController struct {
	mu     sync.Mutex
	log    types.Logger
	drv    *winRing0
	ec     *EC
	m      RegMap
	firm   string
	inited bool

	// 去重缓存：与上次成功写入相同的曲线不再重写
	lastCpu, lastGpu *Curve
}

func newPlatformController(logger types.Logger) Controller {
	return &winController{log: logger, m: Vector16HX}
}

func (c *winController) Init(driverPath string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inited {
		return nil
	}

	drv, err := openWinRing0(driverPath)
	if err != nil {
		return err
	}
	ec := NewEC(drv)

	// 安全校验：确认这确实是一台我们认识的 MSI EC，再获得写入资格。
	// gen-2 MSI EC 在 0xA0 起有 12 字节 ASCII 固件版本（如 "15M1IMS2.111"）。
	firm, err := ec.ReadString(c.m.FirmVerReg, c.m.FirmVerLen)
	if err != nil {
		drv.Close()
		return fmt.Errorf("msifan: 读取 EC 固件版本失败: %w", err)
	}
	if !plausibleFirmVer(firm) {
		drv.Close()
		return fmt.Errorf("msifan: EC 固件版本异常（%q），拒绝启用写入以免误写非 MSI EC", firm)
	}
	mode, err := ec.ReadReg(c.m.FanModeReg)
	if err != nil || !knownFanMode(mode) {
		drv.Close()
		return fmt.Errorf("msifan: 风扇模式寄存器校验失败 (val=0x%02X, err=%v)", mode, err)
	}

	c.drv, c.ec, c.firm, c.inited = drv, ec, strings.TrimSpace(firm), true
	if c.log != nil {
		c.log.Info("msifan: EC 就绪，固件 %s，当前风扇模式 0x%02X", c.firm, mode)
	}
	return nil
}

// plausibleFirmVer MSI EC 固件版本形如 "15M1IMS2.111"：可打印 ASCII 且含 "IMS"。
func plausibleFirmVer(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r > 0x7E {
			return false
		}
	}
	return strings.Contains(s, "IMS")
}

func knownFanMode(v byte) bool {
	return v == FanModeAuto || v == FanModeSilent || v == FanModeBasic || v == FanModeAdvanced
}

func (c *winController) Available() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inited
}

func (c *winController) Status() (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.inited {
		return Status{}, fmt.Errorf("msifan: 未初始化")
	}
	st := Status{Available: true, FirmVer: c.firm}

	var err error
	read := func(reg byte) int {
		if err != nil {
			return 0
		}
		var v byte
		v, err = c.ec.ReadReg(reg)
		return int(v)
	}
	st.CpuTemp = read(c.m.CpuTemp)
	st.GpuTemp = read(c.m.GpuTemp)
	st.CpuSpeed = read(c.m.CpuSpeed)
	st.GpuSpeed = read(c.m.GpuSpeed)
	fb := read(c.m.FullBlastReg)
	if err != nil {
		return Status{}, err
	}
	st.FullBlast = fb&0x80 != 0
	st.CpuRPM = c.readRPM(c.m.CpuRpm)
	st.GpuRPM = c.readRPM(c.m.GpuRpm)
	return st, nil
}

func (c *winController) readRPM(reg byte) int {
	raw, err := c.ec.ReadWordBE(reg)
	if err != nil || raw == 0 {
		return 0
	}
	return c.m.RPMDividend / int(raw)
}

func (c *winController) ApplyCurves(cpu, gpu Curve) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.inited {
		return fmt.Errorf("msifan: 未初始化")
	}
	cpu = ClampToSafe(cpu, CpuDefault, c.m.MaxSpeedUnits)
	gpu = ClampToSafe(gpu, GpuDefault, c.m.MaxSpeedUnits)
	if !cpu.Valid() || !gpu.Valid() {
		return fmt.Errorf("msifan: 曲线非法，拒绝写入")
	}

	if err := c.ensureAdvancedLocked(); err != nil {
		return err
	}

	if c.lastCpu == nil || *c.lastCpu != cpu {
		if err := c.writeCurveLocked(cpu, c.m.CpuCurveSpeed, c.m.CpuTup, c.m.CpuTdown); err != nil {
			c.lastCpu = nil
			return fmt.Errorf("msifan: 写 CPU 曲线失败: %w", err)
		}
		cp := cpu
		c.lastCpu = &cp
		if c.log != nil {
			c.log.Debug("msifan: CPU 曲线已写入 %v", cpu.Speeds)
		}
	}
	if c.lastGpu == nil || *c.lastGpu != gpu {
		if err := c.writeCurveLocked(gpu, c.m.GpuCurveSpeed, c.m.GpuTup, c.m.GpuTdown); err != nil {
			c.lastGpu = nil
			return fmt.Errorf("msifan: 写 GPU 曲线失败: %w", err)
		}
		gp := gpu
		c.lastGpu = &gp
		if c.log != nil {
			c.log.Debug("msifan: GPU 曲线已写入 %v", gpu.Speeds)
		}
	}
	return nil
}

func (c *winController) ensureAdvancedLocked() error {
	mode, err := c.ec.ReadReg(c.m.FanModeReg)
	if err != nil {
		return err
	}
	if mode == FanModeAdvanced {
		return nil
	}
	if err := c.ec.WriteReg(c.m.FanModeReg, FanModeAdvanced); err != nil {
		return err
	}
	if c.log != nil {
		c.log.Info("msifan: 风扇模式 0x%02X → Advanced(0x%02X)", mode, FanModeAdvanced)
	}
	return nil
}

func (c *winController) writeCurveLocked(cv Curve, speedRegs [7]byte, tupRegs, tdownRegs [6]byte) error {
	for i, reg := range speedRegs {
		if err := c.ec.WriteReg(reg, cv.Speeds[i]); err != nil {
			return err
		}
	}
	for i := 0; i < 6; i++ {
		if err := c.ec.WriteReg(tupRegs[i], cv.Tup[i]); err != nil {
			return err
		}
		// OffsetDT 布局：下行阈值寄存器存 Tup-Tdown 差值
		if err := c.ec.WriteReg(tdownRegs[i], cv.Tup[i]-cv.Tdown[i]); err != nil {
			return err
		}
	}
	return nil
}

func (c *winController) SetFullBlast(on bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.inited {
		return fmt.Errorf("msifan: 未初始化")
	}
	v, err := c.ec.ReadReg(c.m.FullBlastReg)
	if err != nil {
		return err
	}
	if on {
		v |= 0x80
	} else {
		v &^= 0x80
	}
	return c.ec.WriteReg(c.m.FullBlastReg, v)
}

func (c *winController) RestoreDefault() error {
	if err := c.SetFullBlast(false); err != nil {
		return err
	}
	return c.ApplyCurves(CpuDefault, GpuDefault)
}

func (c *winController) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.drv != nil {
		c.drv.Close()
		c.drv = nil
	}
	c.inited = false
	c.lastCpu, c.lastGpu = nil, nil
}
