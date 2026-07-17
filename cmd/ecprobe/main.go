// ecprobe 是 MSI EC 访问自检工具：定位 “EC 状态等待超时” 属于哪一层。
// 用法（管理员运行）：ecprobe.exe [WinRing0x64.sys 路径]
// 不带参数时默认取同目录下的 WinRing0x64.sys。
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/TIANLI0/THRM/internal/msifan"
)

func main() {
	fmt.Println("=== THRM MSI EC 自检工具 ===")
	fmt.Println()

	driverPath := ""
	if len(os.Args) > 1 {
		driverPath = os.Args[1]
	} else if exe, err := os.Executable(); err == nil {
		driverPath = filepath.Join(filepath.Dir(exe), "WinRing0x64.sys")
	}

	err := msifan.Diagnose(driverPath, func(s string) { fmt.Println(s) })
	fmt.Println()
	if err != nil {
		fmt.Println("自检返回错误:", err)
	} else {
		fmt.Println("自检完成。请把以上全部输出发回。")
	}
	fmt.Println()
	fmt.Print("按回车退出...")
	fmt.Scanln()
}
