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

// Diagnose 运行一次 EC 访问自检，逐步把结果喂给 logf，用于定位
// “EC 状态等待超时” 属于哪一层（驱动通路 / 端口读 / 写 / EC 协议 / 机型）。
func Diagnose(driverPath string, logf func(string)) error {
	logf(fmt.Sprintf("驱动路径: %q", driverPath))
	drv, err := openWinRing0(driverPath)
	if err != nil {
		logf("打开 WinRing0 失败: " + err.Error())
		return err
	}
	defer drv.Close()
	logf("WinRing0 已打开")

	ver := drv.driverVersion()
	logf(fmt.Sprintf("GetDriverVersion = 0x%08X  (非 0 → IOCTL 读通路正常；0 → 驱动通路本身有问题)", ver))
	logf(fmt.Sprintf("GetRefCount      = %d", drv.refCount()))

	// 裸读状态口，观察 IBF/OBF
	for i := 0; i < 3; i++ {
		v, e := drv.ReadPort(0x66)
		logf(fmt.Sprintf("裸读 0x66(状态口) #%d = 0x%02X  IBF=%d OBF=%d  err=%v",
			i, v, (v>>1)&1, v&1, e))
	}

	m := Vector16HX
	// 手动逐步做一次 EC 读（读 CPU 温度 0x68），每步打印状态口
	logf("--- 手动 EC 读 0x68(CPU温度) ---")
	verboseRead := func(reg byte, name string) {
		step := func(label string) byte {
			st, _ := drv.ReadPort(0x66)
			logf(fmt.Sprintf("  %-16s 状态=0x%02X (IBF=%d OBF=%d)", label, st, (st>>1)&1, st&1))
			return st
		}
		step("发命令前")
		if e := drv.WritePort(0x66, 0x80); e != nil {
			logf("  写 RD_EC(0x80) 失败: " + e.Error())
			return
		}
		step("写0x80后")
		if e := drv.WritePort(0x62, reg); e != nil {
			logf("  写地址失败: " + e.Error())
			return
		}
		st := step("写地址后")
		// 轮询 OBF 最多 ~200ms
		for i := 0; i < 2000 && st&1 == 0; i++ {
			st, _ = drv.ReadPort(0x66)
		}
		val, _ := drv.ReadPort(0x62)
		logf(fmt.Sprintf("  轮询后状态=0x%02X, 读回 %s = 0x%02X (%d)", st, name, val, val))
	}
	verboseRead(m.CpuTemp, "CPU温度")

	// 走正式 EC 接口读固件串
	ec := NewEC(drv)
	s, e := ec.ReadString(m.FirmVerReg, m.FirmVerLen)
	logf(fmt.Sprintf("--- 正式接口 ReadString(0xA0,12) = %q err=%v", s, e))

	// ---- 全寄存器 dump ----
	rb := func(reg byte) string {
		v, err := ec.ReadReg(reg)
		if err != nil {
			return fmt.Sprintf("0x%02X=ERR(%v)", reg, err)
		}
		return fmt.Sprintf("0x%02X=0x%02X(%d)", reg, v, v)
	}
	rlist := func(regs []byte) string {
		out := ""
		for _, r := range regs {
			out += rb(r) + " "
		}
		return out
	}
	logf("--- 全寄存器 dump ---")
	logf("固件版本 " + rb(m.FirmVerReg))
	logf("风扇模式 0xD4 " + rb(m.FanModeReg) + "  (YAMDCC枚举: 0x0D自动/0x1D静音/0x4D基础/0x8D高级)")
	logf("性能模式 0xD2 " + rb(m.PerfModeReg))
	logf("FullBlast 0x98 " + rb(m.FullBlastReg) + "  (bit7=1 表示 Cooler Boost 开)")
	logf("CPU温度 " + rb(m.CpuTemp) + "  GPU温度 " + rb(m.GpuTemp))
	logf("CPU当前速度 " + rb(m.CpuSpeed) + "  GPU当前速度 " + rb(m.GpuSpeed))
	if raw, err := ec.ReadWordBE(m.CpuRpm); err == nil {
		rpm := 0
		if raw > 0 {
			rpm = m.RPMDividend / int(raw)
		}
		logf(fmt.Sprintf("CPU RPM原始=0x%04X → %d RPM", raw, rpm))
	}
	if raw, err := ec.ReadWordBE(m.GpuRpm); err == nil {
		rpm := 0
		if raw > 0 {
			rpm = m.RPMDividend / int(raw)
		}
		logf(fmt.Sprintf("GPU RPM原始=0x%04X → %d RPM", raw, rpm))
	}
	logf("CPU曲线速度点 " + rlist(m.CpuCurveSpeed[:]))
	logf("CPU升温阈值   " + rlist(m.CpuTup[:]))
	logf("CPU降温阈值Δ  " + rlist(m.CpuTdown[:]))
	logf("GPU曲线速度点 " + rlist(m.GpuCurveSpeed[:]))
	logf("GPU升温阈值   " + rlist(m.GpuTup[:]))
	logf("GPU降温阈值Δ  " + rlist(m.GpuTdown[:]))

	// ---- 端到端：跑真实 Init + Status（只读，不写曲线）----
	drv.Close() // 释放句柄，交给正式控制器重新打开
	logf("--- 端到端：真实 Init + Status（只读）---")
	ctl := New(&logfLogger{logf})
	if err := ctl.Init(driverPath); err != nil {
		logf("Init 失败: " + err.Error())
		return nil
	}
	defer ctl.Close()
	logf("Init 成功 ✓ 后端已就绪")
	st, err := ctl.Status()
	if err != nil {
		logf("Status 失败: " + err.Error())
		return nil
	}
	logf(fmt.Sprintf("Status: 固件=%s CPU=%d°C/%dRPM GPU=%d°C/%dRPM CPU速度=%d GPU速度=%d FullBlast=%v",
		st.FirmVer, st.CpuTemp, st.CpuRPM, st.GpuTemp, st.GpuRPM, st.CpuSpeed, st.GpuSpeed, st.FullBlast))
	logf(">>> 若以上 Status 数值合理，说明联动可正常启用 <<<")
	return nil
}

// logfLogger 把 types.Logger 转发到 logf，供 Diagnose 复用正式控制器。
type logfLogger struct{ logf func(string) }

func (l *logfLogger) Info(f string, v ...any)  { l.logf("[INFO] " + fmt.Sprintf(f, v...)) }
func (l *logfLogger) Error(f string, v ...any) { l.logf("[ERR ] " + fmt.Sprintf(f, v...)) }
func (l *logfLogger) Warn(f string, v ...any)  { l.logf("[WARN] " + fmt.Sprintf(f, v...)) }
func (l *logfLogger) Debug(f string, v ...any) { l.logf("[DBG ] " + fmt.Sprintf(f, v...)) }
func (l *logfLogger) Close()                   {}
func (l *logfLogger) CleanOldLogs()            {}
func (l *logfLogger) SetDebugMode(bool)        {}
func (l *logfLogger) GetLogDir() string        { return "" }

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
	if err != nil {
		drv.Close()
		return fmt.Errorf("msifan: 读取风扇模式寄存器失败: %w", err)
	}
	if !knownFanMode(mode) {
		// 不同固件/机型的风扇模式寄存器可能持有 YAMDCC 枚举外的值（本机实测 0x80：
		// 高位 0x8 属 Advanced 族、低位非 0xD 表示手动编辑位未开）。机型身份已由
		// 固件 "IMS" 校验确认，写曲线前 ensureAdvancedLocked 会显式切到 Advanced(0x8D)，
		// 故此处仅记录、不作拒绝条件。
		if c.log != nil {
			c.log.Info("msifan: 风扇模式寄存器值 0x%02X 不在已知枚举内，仍按 MSI EC 启用（写入前会切到 Advanced）", mode)
		}
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
