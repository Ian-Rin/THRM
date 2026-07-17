// Package msifan 通过 EC（嵌入式控制器）直接控制 MSI 笔记本的 CPU/GPU 风扇。
//
// 与 laptopfan 包（只读 WMI 转速）不同，本包具备写入能力：向 EC 写入完整的
// 温度-转速曲线（7 个速度点 + 6 组上/下温度阈值），之后由 EC 固件自主跟随
// 温度调速——即使本进程退出，EC 仍按最后写入的曲线安全运行。
//
// 寄存器布局为 gen-2（"IsNewEC"）MSI EC，数值经社区在
// MSI Vector 16 HX A14VHG（固件 15M1IMS2.111）上验证：
//   - 风扇速度为 EC 原生单位 0..150（100 ≈ 正常满速，>100 为超频段）。
//   - 下行阈值寄存器存的是差值 Tup-Tdown（"OffsetDT" 布局），不是绝对温度。
//   - RPM 寄存器 16 位大端，RPM = 478000 / 原始值。
//   - EC 通过标准 ACPI 端口访问：0x66 命令/状态口，0x62 数据口。
package msifan

// RegMap MSI 笔记本 EC 寄存器布局。
type RegMap struct {
	CpuTemp  byte // 当前 CPU 温度（°C）
	GpuTemp  byte // 当前 GPU 温度（°C）
	CpuSpeed byte // 当前 CPU 风扇目标速度（EC 单位）
	GpuSpeed byte

	CpuRpm byte // 16 位大端；RPM = RPMDividend/raw
	GpuRpm byte

	CpuCurveSpeed [7]byte
	CpuTup        [6]byte
	CpuTdown      [6]byte // 存 Tup-Tdown 差值
	GpuCurveSpeed [7]byte
	GpuTup        [6]byte
	GpuTdown      [6]byte

	FullBlastReg byte // bit7 = Cooler Boost（一键全速）
	FanModeReg   byte
	PerfModeReg  byte
	FirmVerReg   byte // 12 字节 ASCII 固件版本
	FirmVerLen   int

	RPMDividend   int
	MaxSpeedUnits int
}

// EC 风扇模式寄存器取值（FanModeReg）。写自定义曲线前必须处于 Advanced。
const (
	FanModeAuto     = 0x0D
	FanModeSilent   = 0x1D
	FanModeBasic    = 0x4D
	FanModeAdvanced = 0x8D
)

// Vector16HX MSI Vector 16 HX A14VHG 的寄存器映射。
var Vector16HX = RegMap{
	CpuTemp:  0x68,
	GpuTemp:  0x80,
	CpuSpeed: 0x71,
	GpuSpeed: 0x89,

	CpuRpm: 0xC8,
	GpuRpm: 0xCA,

	CpuCurveSpeed: [7]byte{0x72, 0x73, 0x74, 0x75, 0x76, 0x77, 0x78},
	CpuTup:        [6]byte{0x6A, 0x6B, 0x6C, 0x6D, 0x6E, 0x6F},
	CpuTdown:      [6]byte{0x7A, 0x7B, 0x7C, 0x7D, 0x7E, 0x7F},
	GpuCurveSpeed: [7]byte{0x8A, 0x8B, 0x8C, 0x8D, 0x8E, 0x8F, 0x90},
	GpuTup:        [6]byte{0x82, 0x83, 0x84, 0x85, 0x86, 0x87},
	GpuTdown:      [6]byte{0x92, 0x93, 0x94, 0x95, 0x96, 0x97},

	FullBlastReg: 0x98,
	FanModeReg:   0xD4,
	PerfModeReg:  0xD2,
	FirmVerReg:   0xA0,
	FirmVerLen:   12,

	RPMDividend:   478000,
	MaxSpeedUnits: 150,
}
