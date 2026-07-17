package msifan

// Curve 一条 EC 风扇曲线：7 个速度点 + 6 组绝对温度阈值。
// Speeds[0] 是低于 Tup[0] 时的基础速度；Speeds[i] (i>=1) 在温度升破 Tup[i-1]
// 后生效，回落到 Tdown[i-1] 以下时降档。所有温度为绝对 °C；写入 EC 时
// 下行阈值会转换为 Tup-Tdown 差值（OffsetDT 布局）。
type Curve struct {
	Speeds [7]byte
	Tup    [6]byte
	Tdown  [6]byte
}

// Vector 16 HX A14VHG 预设曲线。阈值沿用机型默认配置；
// Silent/Default 的速度点来自社区验证配置，Aggressive 为手动加强档
//（上限 120，低于 EC 满速 150，留给 Cooler Boost）。
var (
	// CPU 风扇阈值（所有 CPU 预设共用）
	cpuTup   = [6]byte{55, 62, 69, 75, 81, 87}
	cpuTdown = [6]byte{40, 49, 56, 62, 68, 74}
	// GPU 风扇阈值
	gpuTup   = [6]byte{55, 60, 65, 70, 75, 80}
	gpuTdown = [6]byte{48, 53, 58, 63, 68, 73}

	CpuSilent     = Curve{Speeds: [7]byte{0, 0, 20, 30, 75, 89, 103}, Tup: cpuTup, Tdown: cpuTdown}
	CpuDefault    = Curve{Speeds: [7]byte{0, 40, 48, 60, 75, 89, 103}, Tup: cpuTup, Tdown: cpuTdown}
	CpuAggressive = Curve{Speeds: [7]byte{0, 48, 58, 72, 88, 104, 120}, Tup: cpuTup, Tdown: cpuTdown}

	GpuSilent     = Curve{Speeds: [7]byte{0, 0, 20, 25, 82, 93, 104}, Tup: gpuTup, Tdown: gpuTdown}
	GpuDefault    = Curve{Speeds: [7]byte{0, 48, 60, 70, 82, 93, 104}, Tup: gpuTup, Tdown: gpuTdown}
	GpuAggressive = Curve{Speeds: [7]byte{0, 55, 68, 80, 94, 106, 120}, Tup: gpuTup, Tdown: gpuTdown}
)

// safetyTailPoints 曲线尾部受保护的点数：最高温的 2 个速度点永远不允许
// 低于机型默认曲线，保证即使联动逻辑出错或本进程死掉，EC 在高温区
// 仍有不弱于出厂的散热响应。
const safetyTailPoints = 2

// Blend 在 a、b 两条曲线之间按 t∈[0,1] 线性插值速度点（阈值取自 a）。
func Blend(a, b Curve, t float64) Curve {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		b.Tup, b.Tdown = a.Tup, a.Tdown
		return b
	}
	out := a
	for i := range out.Speeds {
		va, vb := float64(a.Speeds[i]), float64(b.Speeds[i])
		out.Speeds[i] = byte(va + (vb-va)*t + 0.5)
	}
	return out
}

// ClampToSafe 返回钳制后的曲线：所有速度点限制在 [0, maxUnits]，
// 且尾部 safetyTailPoints 个点不低于 floor 对应点。
func ClampToSafe(c, floor Curve, maxUnits int) Curve {
	for i := range c.Speeds {
		if int(c.Speeds[i]) > maxUnits {
			c.Speeds[i] = byte(maxUnits)
		}
	}
	for i := len(c.Speeds) - safetyTailPoints; i < len(c.Speeds); i++ {
		if c.Speeds[i] < floor.Speeds[i] {
			c.Speeds[i] = floor.Speeds[i]
		}
	}
	return c
}

// Valid 校验曲线的结构合法性：阈值单调递增、Tdown < Tup、速度非降序不强制
// 但必须在 [0,150] 内。
func (c Curve) Valid() bool {
	for i := 0; i < len(c.Tup); i++ {
		if c.Tdown[i] >= c.Tup[i] {
			return false
		}
		if i > 0 && c.Tup[i] <= c.Tup[i-1] {
			return false
		}
		if int(c.Speeds[i]) > 150 {
			return false
		}
	}
	return int(c.Speeds[6]) <= 150
}
