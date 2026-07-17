//go:build windows

package msifan

import (
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// winRing0 通过 WinRing0 内核驱动实现 PortIO。
//
// 驱动接口（OpenLibSys WinRing0 1.2.0，公开 IOCTL 接口）：
//   - 设备名 \\.\WinRing0_1_2_0，内核服务名同名
//   - IOCTL 设备类型 40000 (0x9C40)
//   - ReadIoPortByte:  function 0x833, FILE_READ_ACCESS,  入参 uint32 端口号，出参 uint32
//   - WriteIoPortByte: function 0x836, FILE_WRITE_ACCESS, 入参 pack(1){uint32 端口, byte 值}
//   - GetRefCount:     function 0x801, 出参 uint32 打开计数
//
// 需要管理员权限（SCM 创建/启动内核驱动服务 + 打开设备）。
type winRing0 struct {
	handle    windows.Handle
	installed bool // 本次运行由我们创建了驱动服务（负责清理）
}

const winRing0Name = "WinRing0_1_2_0"

const (
	ioctlReadIoPortByte  = 0x9C40<<16 | 1<<14 | 0x833<<2
	ioctlWriteIoPortByte = 0x9C40<<16 | 2<<14 | 0x836<<2
	ioctlGetRefCount     = 0x9C40<<16 | 0x801<<2
)

// openWinRing0 打开（必要时先安装并启动）WinRing0 驱动。
// driverPath 为 WinRing0x64.sys 的完整路径；若驱动已在运行则忽略。
func openWinRing0(driverPath string) (*winRing0, error) {
	w := &winRing0{handle: windows.InvalidHandle}

	// 先尝试直接打开已存在的设备（YAMDCC 或早前实例可能已装载）
	if err := w.openDevice(); err == nil {
		return w, nil
	}

	if driverPath == "" {
		return nil, fmt.Errorf("msifan: WinRing0 设备不存在且未提供驱动文件路径")
	}
	if _, err := os.Stat(driverPath); err != nil {
		return nil, fmt.Errorf("msifan: 驱动文件不存在: %s", driverPath)
	}
	abs, err := filepath.Abs(driverPath)
	if err != nil {
		return nil, err
	}

	if err := w.installAndStart(abs); err != nil {
		return nil, err
	}
	if err := w.openDevice(); err != nil {
		w.cleanupService()
		return nil, fmt.Errorf("msifan: 驱动已启动但设备打开失败: %w", err)
	}
	return w, nil
}

func (w *winRing0) openDevice() error {
	name, err := windows.UTF16PtrFromString(`\\.\` + winRing0Name)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	w.handle = h
	return nil
}

func (w *winRing0) installAndStart(driverPath string) error {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_ALL_ACCESS)
	if err != nil {
		return fmt.Errorf("msifan: 打开 SCM 失败（需要管理员权限）: %w", err)
	}
	defer windows.CloseServiceHandle(scm)

	nameW, _ := windows.UTF16PtrFromString(winRing0Name)
	pathW, _ := windows.UTF16PtrFromString(driverPath)

	svc, err := windows.CreateService(scm, nameW, nameW,
		windows.SERVICE_ALL_ACCESS, windows.SERVICE_KERNEL_DRIVER,
		windows.SERVICE_DEMAND_START, windows.SERVICE_ERROR_NORMAL,
		pathW, nil, nil, nil, nil, nil)
	if err != nil {
		if err == windows.ERROR_SERVICE_EXISTS || err == windows.ERROR_SERVICE_MARKED_FOR_DELETE {
			svc, err = windows.OpenService(scm, nameW, windows.SERVICE_ALL_ACCESS)
			if err != nil {
				return fmt.Errorf("msifan: 打开已存在的驱动服务失败: %w", err)
			}
		} else {
			return fmt.Errorf("msifan: 创建驱动服务失败: %w", err)
		}
	} else {
		w.installed = true
	}
	defer windows.CloseServiceHandle(svc)

	if err := windows.StartService(svc, 0, nil); err != nil &&
		err != windows.ERROR_SERVICE_ALREADY_RUNNING {
		if w.installed {
			windows.DeleteService(svc)
			w.installed = false
		}
		return fmt.Errorf("msifan: 启动 WinRing0 驱动失败（Defender/内存完整性可能拦截了驱动，见文档）: %w", err)
	}
	return nil
}

// cleanupService 停止并删除本次运行创建的驱动服务。
func (w *winRing0) cleanupService() {
	if !w.installed {
		return
	}
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_ALL_ACCESS)
	if err != nil {
		return
	}
	defer windows.CloseServiceHandle(scm)
	nameW, _ := windows.UTF16PtrFromString(winRing0Name)
	svc, err := windows.OpenService(scm, nameW, windows.SERVICE_ALL_ACCESS)
	if err != nil {
		return
	}
	defer windows.CloseServiceHandle(svc)
	var st windows.SERVICE_STATUS
	windows.ControlService(svc, windows.SERVICE_CONTROL_STOP, &st)
	windows.DeleteService(svc)
	w.installed = false
}

// Close 关闭设备句柄；若驱动无其他使用者且为本进程安装，则卸载驱动服务。
func (w *winRing0) Close() {
	if w.handle != windows.InvalidHandle {
		refs := w.refCount()
		windows.CloseHandle(w.handle)
		w.handle = windows.InvalidHandle
		if refs <= 1 {
			w.cleanupService()
		}
	}
}

func (w *winRing0) refCount() uint32 {
	var out uint32
	var ret uint32
	err := windows.DeviceIoControl(w.handle, ioctlGetRefCount,
		nil, 0, (*byte)(unsafe.Pointer(&out)), 4, &ret, nil)
	if err != nil {
		return 0
	}
	return out
}

func (w *winRing0) ReadPort(port uint16) (byte, error) {
	in := uint32(port)
	var out uint32
	var ret uint32
	err := windows.DeviceIoControl(w.handle, ioctlReadIoPortByte,
		(*byte)(unsafe.Pointer(&in)), 4,
		(*byte)(unsafe.Pointer(&out)), 4, &ret, nil)
	if err != nil {
		return 0, fmt.Errorf("msifan: ReadIoPortByte(0x%X): %w", port, err)
	}
	return byte(out & 0xFF), nil
}

func (w *winRing0) WritePort(port uint16, value byte) error {
	// pack(1) 布局：uint32 端口 + byte 值，共 5 字节
	var in [5]byte
	in[0] = byte(port)
	in[1] = byte(port >> 8)
	in[4] = value
	var ret uint32
	err := windows.DeviceIoControl(w.handle, ioctlWriteIoPortByte,
		&in[0], 5, nil, 0, &ret, nil)
	if err != nil {
		return fmt.Errorf("msifan: WriteIoPortByte(0x%X): %w", port, err)
	}
	return nil
}
