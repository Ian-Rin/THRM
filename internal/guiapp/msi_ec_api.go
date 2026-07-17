package guiapp

import (
	"encoding/json"
	"fmt"

	"github.com/TIANLI0/THRM/internal/ipc"
)

// MsiEcStatus 前端展示用的 MSI EC 风扇状态。
type MsiEcStatus struct {
	Supported bool `json:"supported"`
	Enabled   bool `json:"enabled"`
	Linked    bool `json:"linked"`
	Panic     bool `json:"panic"`
	Status    struct {
		Available bool   `json:"available"`
		FirmVer   string `json:"firmVer"`
		CpuTemp   int    `json:"cpuTemp"`
		GpuTemp   int    `json:"gpuTemp"`
		CpuRPM    int    `json:"cpuRpm"`
		GpuRPM    int    `json:"gpuRpm"`
		CpuSpeed  int    `json:"cpuSpeed"`
		GpuSpeed  int    `json:"gpuSpeed"`
		FullBlast bool   `json:"fullBlast"`
	} `json:"status"`
}

// GetMsiEcStatus 查询 MSI EC 风扇后端状态。
func (a *App) GetMsiEcStatus() MsiEcStatus {
	var status MsiEcStatus
	resp, err := a.sendRequest(ipc.ReqGetMsiEcStatus, nil)
	if err != nil || !resp.Success {
		return status
	}
	json.Unmarshal(resp.Data, &status)
	return status
}

// SetMsiEcFullBlast 手动 Cooler Boost（仅纯监控模式下有效）。
func (a *App) SetMsiEcFullBlast(enabled bool) error {
	resp, err := a.sendRequest(ipc.ReqSetMsiEcFullBlast, ipc.SetBoolParams{Enabled: enabled})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}
