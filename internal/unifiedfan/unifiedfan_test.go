package unifiedfan

import "testing"

func tick(a *Allocator, cpu, gpu float64, coolerRatio float64) Output {
	return a.Tick(Input{
		CpuTemp: cpu, GpuTemp: gpu, DtSeconds: 2,
		CoolerConnected: true, CoolerRPMRatio: coolerRatio,
	})
}

func TestIdleStaysSilent(t *testing.T) {
	a := New(DefaultConfig())
	out := tick(a, 45, 42, 0.3)
	if out.CpuBlend != 0 || out.GpuBlend != 0 {
		t.Fatalf("低温应保持静音曲线: %+v", out)
	}
	if out.FullBlast || out.Panic || out.CoolerOverrideRPM != -1 {
		t.Fatalf("低温不应触发任何直通: %+v", out)
	}
}

func TestHighLoadRampsLaptop(t *testing.T) {
	a := New(DefaultConfig())
	out := tick(a, 88, 70, 0.5)
	if out.CpuBlend <= 0.5 {
		t.Fatalf("CPU 高温应超过 Default 混合: %+v", out)
	}
	if out.GpuBlend >= out.CpuBlend {
		t.Fatalf("GPU 较凉，混合系数应低于 CPU: %+v", out)
	}
}

func TestAssistCreditLowersBlend(t *testing.T) {
	cfg := DefaultConfig()
	a1, a2 := New(cfg), New(cfg)
	noAssist := tick(a1, 75, 60, 0)
	withAssist := tick(a2, 75, 60, 1.0)
	if withAssist.CpuBlend >= noAssist.CpuBlend {
		t.Fatalf("散热器满出力时笔记本混合应更低: %v vs %v", withAssist.CpuBlend, noAssist.CpuBlend)
	}
}

func TestGuardTempCancelsCredit(t *testing.T) {
	cfg := DefaultConfig()
	a1, a2 := New(cfg), New(cfg)
	hot := tick(a1, 86, 60, 1.0)     // 超过 CpuGuardTemp=85
	hotNoCooler := tick(a2, 86, 60, 0)
	if hot.CpuBlend != hotNoCooler.CpuBlend {
		t.Fatalf("温度超守卫后减负积分应作废: %v vs %v", hot.CpuBlend, hotNoCooler.CpuBlend)
	}
}

func TestPanicAndHysteresis(t *testing.T) {
	a := New(DefaultConfig())
	// 单个 tick 的尖峰不应触发 panic
	out := tick(a, 96, 60, 0.5)
	if out.Panic {
		t.Fatal("单 tick 尖峰不应触发 panic")
	}
	out = tick(a, 96, 60, 0.5)
	if !out.Panic || !out.FullBlast || out.CoolerOverrideRPM < 0 {
		t.Fatalf("连续 2 tick 96°C 应触发 panic 直通: %+v", out)
	}
	// 降到 90（高于恢复阈值 88）仍应处于 panic
	out = tick(a, 90, 60, 0.5)
	if !out.Panic {
		t.Fatal("迟滞区间内应维持 panic")
	}
	// 降到 85 以下解除
	out = tick(a, 85, 60, 0.5)
	if out.Panic || out.FullBlast {
		t.Fatalf("低于恢复阈值应解除 panic: %+v", out)
	}
}

func TestCoolerDisconnectCompensation(t *testing.T) {
	a := New(DefaultConfig())
	out := a.Tick(Input{CpuTemp: 50, GpuTemp: 48, DtSeconds: 2, CoolerConnected: false})
	if out.CpuBlend < 0.5 || out.GpuBlend < 0.5 {
		t.Fatalf("散热器断连时应至少回到 Default 曲线: %+v", out)
	}
}

func TestTrendFeedforward(t *testing.T) {
	cfg := DefaultConfig()
	steady, rising := New(cfg), New(cfg)
	// 稳态 70°C
	for i := 0; i < 5; i++ {
		tick(steady, 70, 60, 0.3)
	}
	// 快速升温至 70°C（每 tick +4°C）
	for _, temp := range []float64{54, 58, 62, 66, 70} {
		tick(rising, temp, 60, 0.3)
	}
	sOut := tick(steady, 70, 60, 0.3)
	rOut := tick(rising, 74, 60, 0.3)
	_ = sOut
	// 升温中的分配器在相同温度下需求应更高（用 74 vs 稳态 70+trend≈0 对比略欠公平，
	// 直接对比 70°C 时的输出）：
	s70 := New(cfg)
	for i := 0; i < 5; i++ {
		tick(s70, 70, 60, 0.3)
	}
	r70 := New(cfg)
	for _, temp := range []float64{54, 58, 62, 66} {
		tick(r70, temp, 60, 0.3)
	}
	if got, want := tick(r70, 70, 60, 0.3).CpuBlend, tick(s70, 70, 60, 0.3).CpuBlend; got < want {
		t.Fatalf("快速升温时混合系数应不低于稳态: %v < %v", got, want)
	}
	_ = rOut
}

func TestBlendQuantized(t *testing.T) {
	a := New(DefaultConfig())
	out := tick(a, 78, 65, 0.4)
	q := DefaultConfig().BlendQuantum
	for _, b := range []float64{out.CpuBlend, out.GpuBlend} {
		steps := b / q
		if diff := steps - float64(int(steps+0.5)); diff > 1e-9 || diff < -1e-9 {
			t.Fatalf("混合系数未按 %v 量化: %v", q, b)
		}
	}
}

func TestResetClearsPanic(t *testing.T) {
	a := New(DefaultConfig())
	tick(a, 96, 60, 0.5)
	tick(a, 96, 60, 0.5)
	if !a.InPanic() {
		t.Fatal("应进入 panic")
	}
	a.Reset()
	if a.InPanic() {
		t.Fatal("Reset 后应清除 panic")
	}
}
