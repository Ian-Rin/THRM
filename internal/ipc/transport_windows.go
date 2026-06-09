//go:build windows

package ipc

import (
	"fmt"
	"net"
	"time"

	"github.com/Microsoft/go-winio"
)

func listenIPC() (net.Listener, string, error) {
	cfg := &winio.PipeConfig{
		SecurityDescriptor: "D:P(A;;GA;;;WD)",
	}

	listener, err := winio.ListenPipe(PipePath, cfg)
	if err != nil {
		return nil, "", fmt.Errorf("创建命名管道失败: %v", err)
	}
	return listener, PipePath, nil
}

func dialIPC(endpoint string, timeout time.Duration) (net.Conn, error) {
	t := timeout
	return winio.DialPipe(endpoint, &t)
}

func ipcEndpointFromName(name string) string {
	return `\\.\pipe\` + name
}

