package ipc

import (
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const defaultStressMemoryMB = 256

var stressCPUSink atomic.Uint64

func TestIPCUnderCPUAndMemoryPressure(t *testing.T) {
	if os.Getenv("THRM_STRESS") != "1" {
		t.Skip("set THRM_STRESS=1 to run resource pressure tests")
	}

	memoryMB := defaultStressMemoryMB
	if value, err := strconv.Atoi(os.Getenv("THRM_STRESS_MEMORY_MB")); err == nil && value >= 64 && value <= 2048 {
		memoryMB = value
	}

	runtime.GC()
	debug.FreeOSMemory()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	goroutinesBefore := runtime.NumGoroutine()

	pressure := make([]byte, memoryMB*1024*1024)
	pageSize := os.Getpagesize()
	for offset := 0; offset < len(pressure); offset += pageSize {
		pressure[offset] = byte((offset/pageSize)%251 + 1)
	}
	var underPressure runtime.MemStats
	runtime.ReadMemStats(&underPressure)
	allocatedPressure := underPressure.HeapAlloc - before.HeapAlloc
	if allocatedPressure < uint64(memoryMB)*1024*1024*9/10 {
		t.Fatalf("pressure allocation reached %.2fMB, want at least 90%% of %dMB",
			float64(allocatedPressure)/(1024*1024), memoryMB)
	}

	stopCPU := make(chan struct{})
	var cpuWorkers sync.WaitGroup
	workerCount := max(runtime.GOMAXPROCS(0), 1)
	for worker := 0; worker < workerCount; worker++ {
		cpuWorkers.Add(1)
		go func(seed uint64) {
			defer cpuWorkers.Done()
			value := seed + 1
			for {
				for range 10_000 {
					value = value*6364136223846793005 + 1442695040888963407
				}
				select {
				case <-stopCPU:
					stressCPUSink.Add(value)
					return
				default:
				}
			}
		}(uint64(worker))
	}

	handlerCalls := atomic.Uint64{}
	server := NewServer(func(req Request) Response {
		handlerCalls.Add(1)
		return Response{Success: true}
	}, testLogger{})
	if err := server.Start(); err != nil {
		close(stopCPU)
		cpuWorkers.Wait()
		t.Fatalf("Start error: %v", err)
	}

	client := NewClient(testLogger{})
	if err := client.Connect(); err != nil {
		server.Stop()
		close(stopCPU)
		cpuWorkers.Wait()
		t.Fatalf("Connect error: %v", err)
	}
	if response, err := client.SendRequestWithTimeout(ReqPing, nil, 3*time.Second); err != nil || response == nil || !response.Success {
		client.Close()
		server.Stop()
		close(stopCPU)
		cpuWorkers.Wait()
		t.Fatalf("initial IPC handshake failed: response=%v err=%v", response, err)
	}

	eventsHandled := atomic.Uint64{}
	criticalEventHandled := make(chan struct{}, 1)
	client.SetEventHandler(func(event Event) {
		eventsHandled.Add(1)
		if event.Type == EventHealthPing {
			select {
			case criticalEventHandled <- struct{}{}:
			default:
			}
		}
		time.Sleep(2 * time.Millisecond)
	})

	eventFloodDone := make(chan struct{})
	go func() {
		defer close(eventFloodDone)
		for index := 0; index < 5_000; index++ {
			server.BroadcastEvent(EventTemperatureUpdate, index)
		}
	}()

	const requestWorkers = 8
	const requestsPerWorker = 50
	var requestGroup sync.WaitGroup
	var requestFailures atomic.Uint64
	for worker := 0; worker < requestWorkers; worker++ {
		requestGroup.Add(1)
		go func() {
			defer requestGroup.Done()
			for range requestsPerWorker {
				response, err := client.SendRequestWithTimeout(ReqPing, nil, 3*time.Second)
				if err != nil || response == nil || !response.Success {
					requestFailures.Add(1)
				}
			}
		}()
	}
	requestGroup.Wait()
	<-eventFloodDone
	server.BroadcastEvent(EventHealthPing, time.Now().UnixMilli())
	select {
	case <-criticalEventHandled:
	case <-time.After(3 * time.Second):
		t.Error("critical health event was not delivered after telemetry flood")
	}

	const reconnectCycles = 25
	for cycle := 0; cycle < reconnectCycles; cycle++ {
		client.Close()
		if err := client.Connect(); err != nil {
			t.Fatalf("reconnect cycle %d failed: %v", cycle+1, err)
		}
		response, err := client.SendRequestWithTimeout(ReqPing, nil, 3*time.Second)
		if err != nil || response == nil || !response.Success {
			t.Fatalf("health request after reconnect cycle %d failed: response=%v err=%v", cycle+1, response, err)
		}
	}

	response, finalErr := client.SendRequestWithTimeout(ReqPing, nil, 3*time.Second)
	if finalErr != nil || response == nil || !response.Success {
		t.Errorf("final health request failed under pressure: response=%v err=%v", response, finalErr)
	}
	if failures := requestFailures.Load(); failures != 0 {
		t.Errorf("%d/%d concurrent requests failed under pressure", failures, requestWorkers*requestsPerWorker)
	}
	if eventsHandled.Load() == 0 {
		t.Error("no events were delivered under pressure")
	}

	client.Close()
	server.Stop()
	close(stopCPU)
	cpuWorkers.Wait()
	runtime.KeepAlive(pressure)
	pressure = nil

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > goroutinesBefore+16 && time.Now().Before(deadline) {
		runtime.GC()
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
	runtime.GC()
	debug.FreeOSMemory()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	goroutinesAfter := runtime.NumGoroutine()

	heapGrowth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("pressure=%dMB allocated=%.2fMB cpuWorkers=%d requests=%d reconnects=%d handlerCalls=%d eventsHandled=%d heapGrowth=%.2fMB goroutines=%d->%d",
		memoryMB,
		float64(allocatedPressure)/(1024*1024),
		workerCount,
		requestWorkers*requestsPerWorker+reconnectCycles+2,
		reconnectCycles,
		handlerCalls.Load(),
		eventsHandled.Load(),
		float64(heapGrowth)/(1024*1024),
		goroutinesBefore,
		goroutinesAfter,
	)

	if heapGrowth > 32*1024*1024 {
		t.Errorf("live heap remained %.2fMB above baseline after pressure release", float64(heapGrowth)/(1024*1024))
	}
	if goroutinesAfter > goroutinesBefore+16 {
		t.Errorf("goroutine count did not settle after pressure: before=%d after=%d", goroutinesBefore, goroutinesAfter)
	}
}
