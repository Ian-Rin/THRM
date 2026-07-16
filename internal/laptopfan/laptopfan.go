// Package laptopfan 读取笔记本内置 CPU/GPU 风扇转速。
//
// 目前支持 Uniwill/同方准系统（机械革命等品牌）：这类机型的 ACPI 固件在
// root\WMI 下暴露 AcpiTest_MULong 类（GUID ABBC0F6F-8EA1-11D1-00A0-C90629100000），
// 其 GetSetULong 方法可读写 EC RAM。风扇转速寄存器为 16 位大端：
//   0x0464 → 风扇1（CPU），0x046C → 风扇2（GPU）
// 寄存器布局与 Linux 侧 qc71_laptop / tuxedo-drivers 一致。
package laptopfan

import "github.com/TIANLI0/THRM/internal/types"

// FanSpeeds 笔记本内置风扇转速读数。
type FanSpeeds struct {
	CPUFanRPM int
	GPUFanRPM int
}

// Reader 笔记本风扇转速读取器。非 Windows 平台或不支持的机型上所有方法安全返回零值。
type Reader struct {
	impl readerImpl
}

type readerImpl interface {
	read() (FanSpeeds, bool)
}

// NewReader 创建读取器。探测在首次读取时惰性完成。
func NewReader(logger types.Logger) *Reader {
	return &Reader{impl: newPlatformReader(logger)}
}

// Read 读取当前转速。ok=false 表示本机不支持或读取失败。
func (r *Reader) Read() (FanSpeeds, bool) {
	if r == nil || r.impl == nil {
		return FanSpeeds{}, false
	}
	return r.impl.read()
}
