// Package unifiedfan 统一热管理分配器：把 MSI 笔记本双风扇与飞智散热器
// 协调成一个整体。
//
// 分工边界：散热器转速仍由 THRM 既有管线（用户曲线 + 学习偏移 + 预测前馈 +
// 转速避让）决定，本包不干预；本包负责笔记本侧——根据 CPU/GPU 温度与
// 散热器当前出力，决定笔记本风扇曲线的混合系数，并处理两侧的协调事件
//（高温 panic 直通、散热器断连补偿）。
//
// 设计原则（噪音最优）：
//   - 散热器压风安静 → 让它先出力；散热器出力越大，笔记本风扇获得越多
//     "减负积分"，曲线向静音方向偏移（带温度守卫，接近过热时积分作废）；
//   - 笔记本侧输出的是曲线混合系数（0=Silent 0.5=Default 1=Aggressive），
//     实际由 EC 固件自主执行曲线，软件死掉也安全；
//   - 温度趋势前馈：快速升温时提前加大混合系数；
//   - panic：CPU/GPU 逼近极限 → Cooler Boost + 散热器满速直通，迟滞恢复；
//   - 散热器断连/未接 → 笔记本曲线至少回到 Default，补偿失去的风量。
//
// 本包为纯逻辑，无平台依赖，全部可单测。
package unifiedfan

import "math"

// Config 分配器参数（默认值见 DefaultConfig）。
type Config struct {
	// 需求映射：温度在 [TempZero, TempFull] 区间线性映射到需求 0..1
	CpuTempZero, CpuTempFull float64
	GpuTempZero, GpuTempFull float64

	// 趋势前馈：需求 += TrendGain * max(0, dT/dt °C/s)
	TrendGain float64

	// 笔记本介入拐点：需求低于该值时保持静音曲线
	LaptopKnee float64
	// 散热器满出力时给笔记本需求的最大减负量
	AssistCredit float64
	// 温度守卫：达到守卫温度后减负积分作废
	CpuGuardTemp, GpuGuardTemp float64
	// 混合系数量化步长（减少 EC 曲线重写次数）
	BlendQuantum float64

	// panic 直通（Cooler Boost + 散热器满速），带迟滞恢复
	CpuPanicTemp, CpuRecoverTemp float64
	GpuPanicTemp, GpuRecoverTemp float64
}

// DefaultConfig 面向 Vector 16 HX + BS 系列散热器的默认参数。
func DefaultConfig() Config {
	return Config{
		CpuTempZero: 45, CpuTempFull: 92,
		GpuTempZero: 45, GpuTempFull: 85,
		TrendGain: 0.12,

		LaptopKnee:   0.32,
		AssistCredit: 0.10,
		CpuGuardTemp: 85, GpuGuardTemp: 80,
		BlendQuantum: 0.05,

		CpuPanicTemp: 95, CpuRecoverTemp: 88,
		GpuPanicTemp: 87, GpuRecoverTemp: 81,
	}
}

// Input 每个 tick 的输入。
type Input struct {
	CpuTemp, GpuTemp float64 // 已平滑的温度，°C
	DtSeconds        float64 // 距上个 tick 的秒数
	CoolerConnected  bool
	CoolerRPMRatio   float64 // 散热器当前目标转速 / 满速，0..1
}

// Output 每个 tick 的输出。
type Output struct {
	// 笔记本曲线混合系数 0..1：0=Silent，0.5=Default，1=Aggressive。
	// 调用方用 msifan.Blend 两段插值生成实际曲线。
	CpuBlend, GpuBlend float64
	FullBlast          bool // panic：开启 Cooler Boost
	// panic 时要求散热器满速直通；-1 表示不干预散热器
	CoolerOverrideRPM int
	Panic             bool
}

// Allocator 有状态的统一分配器。
type Allocator struct {
	cfg Config

	prevCpuTemp, prevGpuTemp float64
	trendCpu, trendGpu       float64 // EMA 平滑的升温速率
	hasPrev                  bool
	inPanic                  bool
	panicArm                 int // 连续满足 panic 条件的 tick 数
}

// panicArmTicks 连续多少个 tick 满足条件才进入 panic（过滤瞬时温度尖峰）。
const panicArmTicks = 2

// New 创建分配器。
func New(cfg Config) *Allocator {
	return &Allocator{cfg: cfg}
}

// Reset 清空趋势/panic 状态（联动开关切换或长时间暂停后调用）。
func (a *Allocator) Reset() {
	a.hasPrev = false
	a.trendCpu, a.trendGpu = 0, 0
	a.inPanic = false
	a.panicArm = 0
}

// InPanic 当前是否处于 panic 直通状态。
func (a *Allocator) InPanic() bool { return a.inPanic }

// Tick 计算本周期的笔记本风扇分配。
func (a *Allocator) Tick(in Input) Output {
	c := a.cfg

	// ---- 升温趋势（EMA 平滑，只取正向）----
	if a.hasPrev && in.DtSeconds > 0 {
		const alpha = 0.3
		rc := (in.CpuTemp - a.prevCpuTemp) / in.DtSeconds
		rg := (in.GpuTemp - a.prevGpuTemp) / in.DtSeconds
		a.trendCpu = a.trendCpu*(1-alpha) + rc*alpha
		a.trendGpu = a.trendGpu*(1-alpha) + rg*alpha
	}
	a.prevCpuTemp, a.prevGpuTemp, a.hasPrev = in.CpuTemp, in.GpuTemp, true

	// ---- panic 直通（连续确认进入 + 迟滞恢复）----
	if in.CpuTemp >= c.CpuPanicTemp || in.GpuTemp >= c.GpuPanicTemp {
		a.panicArm++
		if a.panicArm >= panicArmTicks {
			a.inPanic = true
		}
	} else {
		a.panicArm = 0
		if in.CpuTemp <= c.CpuRecoverTemp && in.GpuTemp <= c.GpuRecoverTemp {
			a.inPanic = false
		}
	}
	if a.inPanic {
		return Output{CpuBlend: 1, GpuBlend: 1, FullBlast: true, CoolerOverrideRPM: coolerMaxHint, Panic: true}
	}

	// ---- 每源需求（温度映射 + 趋势前馈）----
	dCpu := clamp01(ramp(in.CpuTemp, c.CpuTempZero, c.CpuTempFull) + c.TrendGain*math.Max(0, a.trendCpu))
	dGpu := clamp01(ramp(in.GpuTemp, c.GpuTempZero, c.GpuTempFull) + c.TrendGain*math.Max(0, a.trendGpu))

	ratio := 0.0
	if in.CoolerConnected {
		ratio = clamp01(in.CoolerRPMRatio)
	}
	return Output{
		CpuBlend:          a.blend(dCpu, ratio, in.CpuTemp, c.CpuGuardTemp, in.CoolerConnected),
		GpuBlend:          a.blend(dGpu, ratio, in.GpuTemp, c.GpuGuardTemp, in.CoolerConnected),
		CoolerOverrideRPM: -1,
	}
}

// coolerMaxHint panic 时散热器直通转速的占位值；调用方应换成设备实际满速。
const coolerMaxHint = 1 << 30

// blend 计算单风扇曲线混合系数。
func (a *Allocator) blend(demand, coolerRatio, temp, guard float64, coolerConnected bool) float64 {
	c := a.cfg
	d := demand
	if coolerRatio > 0 && temp < guard {
		d -= c.AssistCredit * coolerRatio
	}
	b := 0.0
	if d > c.LaptopKnee {
		b = (d - c.LaptopKnee) / (1 - c.LaptopKnee)
	}
	// 散热器断连时的失援补偿：至少 Default 曲线
	if !coolerConnected {
		b = math.Max(b, 0.5)
	}
	b = clamp01(b)
	if c.BlendQuantum > 0 {
		b = math.Round(b/c.BlendQuantum) * c.BlendQuantum
	}
	return clamp01(b)
}

func ramp(v, zero, full float64) float64 {
	if full <= zero {
		return 0
	}
	return (v - zero) / (full - zero)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
