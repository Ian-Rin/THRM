package msifan

import "github.com/TIANLI0/THRM/internal/types"

// Status MSI 笔记本风扇/温度快照。
type Status struct {
	Available bool   `json:"available"`
	FirmVer   string `json:"firmVer"`
	CpuTemp   int    `json:"cpuTemp"`
	GpuTemp   int    `json:"gpuTemp"`
	CpuRPM    int    `json:"cpuRpm"`
	GpuRPM    int    `json:"gpuRpm"`
	CpuSpeed  int    `json:"cpuSpeed"` // 当前目标速度（EC 单位 0..150）
	GpuSpeed  int    `json:"gpuSpeed"`
	FullBlast bool   `json:"fullBlast"`
}

// Controller MSI EC 风扇控制器。非 Windows 平台或未检测到受支持的 EC 时，
// Available 返回 false，其余方法安全返回零值/错误。
type Controller interface {
	// Init 装载驱动并校验 EC。driverPath 为 WinRing0x64.sys 路径。
	Init(driverPath string) error
	Available() bool
	Status() (Status, error)
	// ApplyCurves 将 CPU/GPU 曲线写入 EC（带安全钳制与去重缓存）。
	ApplyCurves(cpu, gpu Curve) error
	SetFullBlast(on bool) error
	// RestoreDefault 恢复机型默认曲线并关闭 Full Blast。
	RestoreDefault() error
	Close()
}

// New 创建当前平台的控制器实现。
func New(logger types.Logger) Controller {
	return newPlatformController(logger)
}
