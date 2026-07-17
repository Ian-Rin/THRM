package coreapp

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/TIANLI0/THRM/internal/ipc"
	"github.com/TIANLI0/THRM/internal/msifan"
	"github.com/TIANLI0/THRM/internal/types"
	"github.com/TIANLI0/THRM/internal/unifiedfan"
)

// MSI EC 风扇联动：msifan 后端（EC 直控）+ unifiedfan 分配器（统一热管理）。
//
// 分工：散热器转速仍由既有智能控温管线决定；本模块根据 CPU/GPU 温度与
// 散热器当前出力，把笔记本风扇曲线在 Silent/Default/Aggressive 之间混合后
// 写入 EC，由 EC 固件自主执行。panic（逼近温度极限）时开启 Cooler Boost，
// 并在智能控温开启时要求散热器满速直通。

// msiEcStatusPayload ReqGetMsiEcStatus 的响应体。
type msiEcStatusPayload struct {
	Supported bool          `json:"supported"` // 后端已初始化成功
	Enabled   bool          `json:"enabled"`
	Linked    bool          `json:"linked"`
	Panic     bool          `json:"panic"`
	Status    msifan.Status `json:"status"`
}

// startMsiEcDetection 启动时按配置异步初始化 MSI EC 后端。
func (a *CoreApp) startMsiEcDetection() {
	a.safeGo("detectMsiEcSupport", func() {
		cfg := a.configManager.Get()
		if !cfg.MsiEcFan.Enabled {
			a.logDebug("MSI EC 风扇后端未启用，跳过初始化")
			return
		}
		a.initMsiEc(cfg.MsiEcFan)
	})
}

// initMsiEc 装载驱动并校验 EC；幂等。
func (a *CoreApp) initMsiEc(cfg types.MsiEcFanConfig) {
	a.msiMu.Lock()
	defer a.msiMu.Unlock()
	if a.msiFanReady.Load() {
		return
	}
	driverPath := cfg.DriverPath
	if driverPath == "" {
		if exe, err := os.Executable(); err == nil {
			driverPath = filepath.Join(filepath.Dir(exe), "WinRing0x64.sys")
		}
	}
	if err := a.msiFan.Init(driverPath); err != nil {
		a.logError("MSI EC 后端初始化失败: %v", err)
		a.broadcastMsiEcSupport(false, err.Error())
		return
	}
	a.msiAlloc.Reset()
	a.msiFullBlast = false
	a.msiLastTick = time.Time{}
	a.msiFanReady.Store(true)
	if st, err := a.msiFan.Status(); err == nil {
		a.logInfo("MSI EC 后端就绪：固件 %s，CPU %d°C/%dRPM，GPU %d°C/%dRPM",
			st.FirmVer, st.CpuTemp, st.CpuRPM, st.GpuTemp, st.GpuRPM)
	}
	a.broadcastMsiEcSupport(true, "")
}

// shutdownMsiEc 关闭后端；restore=true 时先恢复默认曲线并关闭 Cooler Boost。
func (a *CoreApp) shutdownMsiEc(restore bool) {
	a.msiMu.Lock()
	defer a.msiMu.Unlock()
	if !a.msiFanReady.Load() {
		return
	}
	if restore {
		if err := a.msiFan.RestoreDefault(); err != nil {
			a.logError("MSI EC 恢复默认曲线失败: %v", err)
		} else {
			a.logInfo("MSI EC 已恢复默认曲线")
		}
	}
	a.msiFan.Close()
	a.msiFanReady.Store(false)
	a.msiFullBlast = false
	a.msiAlloc.Reset()
}

// applyMsiEcConfig 配置变更后协调后端状态（在 UpdateConfig 内调用）。
func (a *CoreApp) applyMsiEcConfig(cfg types.MsiEcFanConfig) {
	ready := a.msiFanReady.Load()
	switch {
	case cfg.Enabled && !ready:
		a.safeGo("initMsiEc@config", func() { a.initMsiEc(cfg) })
	case !cfg.Enabled && ready:
		a.safeGo("shutdownMsiEc@config", func() {
			a.shutdownMsiEc(true)
			a.broadcastMsiEcSupport(false, "")
		})
	case cfg.Enabled && ready && !cfg.Linked:
		// 联动关闭转纯监控：恢复默认曲线，之后 tick 不再写入
		a.safeGo("msiEcUnlink@config", func() {
			a.msiMu.Lock()
			defer a.msiMu.Unlock()
			if !a.msiFanReady.Load() {
				return
			}
			if a.msiFullBlast {
				if err := a.msiFan.SetFullBlast(false); err == nil {
					a.msiFullBlast = false
				}
			}
			if err := a.msiFan.RestoreDefault(); err != nil {
				a.logError("MSI EC 恢复默认曲线失败: %v", err)
			}
			a.msiAlloc.Reset()
		})
	}
}

func (a *CoreApp) broadcastMsiEcSupport(supported bool, errMsg string) {
	if a.ipcServer == nil {
		return
	}
	a.ipcServer.BroadcastEvent(ipc.EventMsiEcSupportUpdate, map[string]any{
		"supported": supported,
		"error":     errMsg,
	})
}

// msiEcRPMs 供监控循环把 MSI 双风扇转速并入温度数据显示（laptopfan 的 MSI 版）。
func (a *CoreApp) msiEcRPMs() (cpuRPM, gpuRPM int, ok bool) {
	if !a.msiFanReady.Load() {
		return 0, 0, false
	}
	a.msiMu.Lock()
	defer a.msiMu.Unlock()
	st, err := a.msiFan.Status()
	if err != nil {
		return 0, 0, false
	}
	return st.CpuRPM, st.GpuRPM, true
}

// runMsiEcTick 联动控制主入口，每个温控 tick 末尾调用（无论散热器是否连接）。
func (a *CoreApp) runMsiEcTick(temp types.TemperatureData, cfg types.AppConfig) {
	if !a.msiFanReady.Load() || !cfg.MsiEcFan.Enabled || !cfg.MsiEcFan.Linked {
		return
	}

	coolerConnected := a.deviceManager.IsConnected()
	coolerRPM := 0
	if coolerConnected {
		if fanData := a.deviceManager.GetCurrentFanData(); fanData != nil {
			coolerRPM = int(fanData.CurrentRPM)
		}
	}

	override := a.msiEcTickLocked(temp, coolerConnected, coolerRPM)

	// panic 直通：智能控温开启时把散热器推到满速（手动挡位下不抢用户控制权）
	if override > 0 && cfg.AutoControl && coolerConnected {
		if _, written := a.setAutomaticFanSpeed(override); written {
			a.logInfo("统一热管理 panic：散热器直通 %d RPM", override)
		}
	}
}

// msiEcTickLocked 计算并写入本 tick 的笔记本曲线；返回散热器直通转速（-1 不干预）。
func (a *CoreApp) msiEcTickLocked(temp types.TemperatureData, coolerConnected bool, coolerRPM int) int {
	a.msiMu.Lock()
	defer a.msiMu.Unlock()
	if !a.msiFanReady.Load() {
		return -1
	}

	now := time.Now()
	dt := 0.0
	if !a.msiLastTick.IsZero() {
		dt = now.Sub(a.msiLastTick).Seconds()
	}
	a.msiLastTick = now
	// 长间隔（睡眠恢复、监控暂停）后趋势数据已失效
	if dt > 30 {
		a.msiAlloc.Reset()
		dt = 0
	}

	cpuT, gpuT := float64(temp.CPUTemp), float64(temp.GPUTemp)
	if cpuT <= 0 {
		// 桥接温度缺失时回退 EC 自带温度
		if st, err := a.msiFan.Status(); err == nil {
			cpuT = float64(st.CpuTemp)
			if gpuT <= 0 {
				gpuT = float64(st.GpuTemp)
			}
		}
	}
	if cpuT <= 0 {
		return -1 // 完全无温度数据，本 tick 不动作
	}
	// gpuT<=0（独显休眠/停用监测）按无需求处理，GPU 风扇走静音曲线

	ratio := 0.0
	if coolerConnected && coolerRPM > 0 {
		ratio = float64(coolerRPM) / float64(types.ManualGearMaxRPM)
	}
	out := a.msiAlloc.Tick(unifiedfan.Input{
		CpuTemp:         cpuT,
		GpuTemp:         gpuT,
		DtSeconds:       dt,
		CoolerConnected: coolerConnected,
		CoolerRPMRatio:  ratio,
	})

	cpuCurve := msifan.BlendTri(msifan.CpuSilent, msifan.CpuDefault, msifan.CpuAggressive, out.CpuBlend)
	gpuCurve := msifan.BlendTri(msifan.GpuSilent, msifan.GpuDefault, msifan.GpuAggressive, out.GpuBlend)
	if err := a.msiFan.ApplyCurves(cpuCurve, gpuCurve); err != nil {
		a.logError("MSI EC 曲线写入失败: %v", err)
	}

	if out.FullBlast != a.msiFullBlast {
		if err := a.msiFan.SetFullBlast(out.FullBlast); err != nil {
			a.logError("MSI EC Cooler Boost 切换失败: %v", err)
		} else {
			a.msiFullBlast = out.FullBlast
			if out.FullBlast {
				a.logInfo("统一热管理 panic：开启 Cooler Boost（CPU %.0f°C / GPU %.0f°C）", cpuT, gpuT)
			} else {
				a.logInfo("统一热管理 panic 解除，关闭 Cooler Boost")
			}
		}
	}

	if out.Panic {
		return types.ManualGearMaxRPM
	}
	return -1
}

// GetMsiEcStatus ReqGetMsiEcStatus 处理函数。
func (a *CoreApp) GetMsiEcStatus() msiEcStatusPayload {
	cfg := a.configManager.Get()
	payload := msiEcStatusPayload{
		Enabled: cfg.MsiEcFan.Enabled,
		Linked:  cfg.MsiEcFan.Linked,
	}
	if !a.msiFanReady.Load() {
		return payload
	}
	a.msiMu.Lock()
	defer a.msiMu.Unlock()
	payload.Supported = true
	payload.Panic = a.msiAlloc.InPanic()
	if st, err := a.msiFan.Status(); err == nil {
		payload.Status = st
	}
	return payload
}

// SetMsiEcFullBlast 手动 Cooler Boost（仅纯监控模式下允许，联动模式由算法接管）。
func (a *CoreApp) SetMsiEcFullBlast(on bool) error {
	cfg := a.configManager.Get()
	if !a.msiFanReady.Load() {
		return fmt.Errorf("MSI EC 后端未就绪")
	}
	if cfg.MsiEcFan.Linked {
		return fmt.Errorf("联动模式下 Cooler Boost 由统一热管理接管，请先关闭联动")
	}
	a.msiMu.Lock()
	defer a.msiMu.Unlock()
	if err := a.msiFan.SetFullBlast(on); err != nil {
		return err
	}
	a.msiFullBlast = on
	return nil
}
