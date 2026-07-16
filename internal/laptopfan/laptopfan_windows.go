//go:build windows

package laptopfan

import (
	"fmt"
	"runtime"
	"strconv"
	"sync"

	"github.com/TIANLI0/THRM/internal/types"
	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

const (
	// Uniwill EC RAM 风扇转速寄存器（16 位大端），与 qc71_laptop 的
	// FAN_RPM_1_ADDR / FAN_RPM_2_ADDR 一致。
	ecRegFan1RPM = 0x0464
	ecRegFan2RPM = 0x046C

	// GetSetULong 输入 u64 的第 5 字节为功能码：1=读 EC。
	uniwillFunctionRead = 1

	// WMI 读失败时固件返回的哨兵值。
	uniwillReadErrorValue = 0xfefefefe

	// 转速合理性上限，超过视为无效读数。
	maxReasonableRPM = 12000

	// 连续失败多少次后永久标记为不支持，停止继续尝试。
	maxConsecutiveFailures = 3
)

type windowsReader struct {
	logger types.Logger

	mutex       sync.Mutex
	failures    int
	unsupported bool
	loggedOnce  bool
}

func newPlatformReader(logger types.Logger) readerImpl {
	return &windowsReader{logger: logger}
}

func (r *windowsReader) read() (FanSpeeds, bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.unsupported {
		return FanSpeeds{}, false
	}

	speeds, err := readUniwillFanSpeeds()
	if err != nil {
		r.failures++
		if r.failures >= maxConsecutiveFailures {
			r.unsupported = true
			if r.logger != nil {
				r.logger.Info("笔记本风扇转速读取不可用（Uniwill WMI EC）: %v", err)
			}
		}
		return FanSpeeds{}, false
	}

	r.failures = 0
	if !r.loggedOnce {
		r.loggedOnce = true
		if r.logger != nil {
			r.logger.Info("已启用笔记本内置风扇转速读取（Uniwill WMI EC）: CPU=%d RPM, GPU=%d RPM", speeds.CPUFanRPM, speeds.GPUFanRPM)
		}
	}
	return speeds, true
}

// readUniwillFanSpeeds 通过 root\WMI 的 AcpiTest_MULong.GetSetULong（ExecMethod）
// 读取 EC RAM。每次调用独立完成 COM 初始化，避免跨 goroutine 的公寓线程问题；
// 调用频率为温度采样节奏（≥1s），开销可忽略。
func readUniwillFanSpeeds() (speeds FanSpeeds, err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if initErr := ole.CoInitializeEx(0, ole.COINIT_MULTITHREADED); initErr != nil {
		oleErr, ok := initErr.(*ole.OleError)
		// S_FALSE / RPC_E_CHANGED_MODE：线程已初始化，可继续使用。
		if !ok || (oleErr.Code() != 0x00000001 && oleErr.Code() != 0x80010106) {
			return FanSpeeds{}, fmt.Errorf("CoInitializeEx: %w", initErr)
		}
	} else {
		defer ole.CoUninitialize()
	}

	locatorObj, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		return FanSpeeds{}, fmt.Errorf("创建 SWbemLocator 失败: %w", err)
	}
	defer locatorObj.Release()

	locator, err := locatorObj.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return FanSpeeds{}, fmt.Errorf("SWbemLocator IDispatch: %w", err)
	}
	defer locator.Release()

	serviceRaw, err := oleutil.CallMethod(locator, "ConnectServer", ".", `root\WMI`)
	if err != nil {
		return FanSpeeds{}, fmt.Errorf("连接 root\\WMI 失败: %w", err)
	}
	service := serviceRaw.ToIDispatch()
	defer service.Release()

	resultRaw, err := oleutil.CallMethod(service, "ExecQuery", "SELECT * FROM AcpiTest_MULong")
	if err != nil {
		return FanSpeeds{}, fmt.Errorf("查询 AcpiTest_MULong 失败: %w", err)
	}
	resultSet := resultRaw.ToIDispatch()
	defer resultSet.Release()

	itemRaw, err := oleutil.CallMethod(resultSet, "ItemIndex", 0)
	if err != nil {
		return FanSpeeds{}, fmt.Errorf("AcpiTest_MULong 无实例: %w", err)
	}
	item := itemRaw.ToIDispatch()
	defer item.Release()

	pathRaw, err := oleutil.GetProperty(item, "Path_")
	if err != nil {
		return FanSpeeds{}, fmt.Errorf("读取实例 Path_ 失败: %w", err)
	}
	pathObj := pathRaw.ToIDispatch()
	relPathRaw, err := oleutil.GetProperty(pathObj, "RelPath")
	pathObj.Release()
	if err != nil {
		return FanSpeeds{}, fmt.Errorf("读取实例 RelPath 失败: %w", err)
	}
	relPath := relPathRaw.ToString()

	classRaw, err := oleutil.CallMethod(service, "Get", "AcpiTest_MULong")
	if err != nil {
		return FanSpeeds{}, fmt.Errorf("获取类定义失败: %w", err)
	}
	class := classRaw.ToIDispatch()
	defer class.Release()

	methodsRaw, err := oleutil.GetProperty(class, "Methods_")
	if err != nil {
		return FanSpeeds{}, fmt.Errorf("读取 Methods_ 失败: %w", err)
	}
	methods := methodsRaw.ToIDispatch()
	defer methods.Release()

	methodRaw, err := oleutil.CallMethod(methods, "Item", "GetSetULong")
	if err != nil {
		return FanSpeeds{}, fmt.Errorf("类未提供 GetSetULong 方法: %w", err)
	}
	method := methodRaw.ToIDispatch()
	defer method.Release()

	inDefRaw, err := oleutil.GetProperty(method, "InParameters")
	if err != nil {
		return FanSpeeds{}, fmt.Errorf("读取 InParameters 失败: %w", err)
	}
	inDef := inDefRaw.ToIDispatch()
	defer inDef.Release()

	cpuRPM, err := readEC16(service, inDef, relPath, ecRegFan1RPM)
	if err != nil {
		return FanSpeeds{}, err
	}
	gpuRPM, err := readEC16(service, inDef, relPath, ecRegFan2RPM)
	if err != nil {
		return FanSpeeds{}, err
	}

	if cpuRPM > maxReasonableRPM || gpuRPM > maxReasonableRPM {
		return FanSpeeds{}, fmt.Errorf("转速读数超出合理范围: %d/%d", cpuRPM, gpuRPM)
	}
	return FanSpeeds{CPUFanRPM: cpuRPM, GPUFanRPM: gpuRPM}, nil
}

// readEC16 读取 16 位大端 EC 寄存器。单次 GetSetULong 返回 addr（低字节）与
// addr+1（高字节）两个连续字节，因此一次调用即可拼出完整数值。
func readEC16(service, inDef *ole.IDispatch, relPath string, addr uint16) (int, error) {
	inRaw, err := oleutil.CallMethod(inDef, "SpawnInstance_")
	if err != nil {
		return 0, fmt.Errorf("SpawnInstance_ 失败: %w", err)
	}
	in := inRaw.ToIDispatch()
	defer in.Release()

	// 输入 u64：byte0=addr_low, byte1=addr_high, byte5=功能码(1=读)。
	// CIM uint64 经 IDispatch 自动化传输时以十进制字符串表示。
	data := uint64(addr) | uint64(uniwillFunctionRead)<<40
	if _, err := oleutil.PutProperty(in, "Data", strconv.FormatUint(data, 10)); err != nil {
		return 0, fmt.Errorf("设置 Data 参数失败: %w", err)
	}

	outRaw, err := oleutil.CallMethod(service, "ExecMethod", relPath, "GetSetULong", in)
	if err != nil {
		return 0, fmt.Errorf("GetSetULong(0x%04x) 失败: %w", addr, err)
	}
	outObj := outRaw.ToIDispatch()
	defer outObj.Release()

	retRaw, err := oleutil.GetProperty(outObj, "Return")
	if err != nil {
		return 0, fmt.Errorf("GetSetULong(0x%04x) 缺少返回值: %w", addr, err)
	}
	defer retRaw.Clear()

	value, err := variantToUint32(retRaw)
	if err != nil {
		return 0, fmt.Errorf("GetSetULong(0x%04x) 返回值异常: %w", addr, err)
	}
	if value == uniwillReadErrorValue {
		return 0, fmt.Errorf("EC 读取错误（0x%04x 返回哨兵值）", addr)
	}

	dataLow := int(value & 0xff)         // EC[addr]
	dataHigh := int((value >> 8) & 0xff) // EC[addr+1]
	return dataLow<<8 | dataHigh, nil
}

func variantToUint32(v *ole.VARIANT) (uint32, error) {
	switch value := v.Value().(type) {
	case int32:
		return uint32(value), nil
	case uint32:
		return value, nil
	case int64:
		return uint32(value), nil
	case uint64:
		return uint32(value), nil
	case int:
		return uint32(value), nil
	case string:
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return 0, err
		}
		return uint32(parsed), nil
	default:
		return 0, fmt.Errorf("未知返回类型 %T", value)
	}
}
