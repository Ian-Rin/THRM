package msifan

import "testing"

func TestPresetCurvesValid(t *testing.T) {
	for name, c := range map[string]Curve{
		"CpuSilent": CpuSilent, "CpuDefault": CpuDefault, "CpuAggressive": CpuAggressive,
		"GpuSilent": GpuSilent, "GpuDefault": GpuDefault, "GpuAggressive": GpuAggressive,
	} {
		if !c.Valid() {
			t.Errorf("%s 预设曲线非法: %+v", name, c)
		}
	}
}

func TestBlendEndpoints(t *testing.T) {
	if got := Blend(CpuSilent, CpuAggressive, 0); got != CpuSilent {
		t.Fatal("t=0 应返回曲线 a")
	}
	got := Blend(CpuSilent, CpuAggressive, 1)
	if got.Speeds != CpuAggressive.Speeds || got.Tup != CpuSilent.Tup {
		t.Fatal("t=1 应返回 b 的速度 + a 的阈值")
	}
}

func TestBlendMidpoint(t *testing.T) {
	got := Blend(CpuSilent, CpuAggressive, 0.5)
	// 中点 = 两端速度点的算术平均（四舍五入），随预设值自动推导
	want := byte((float64(CpuSilent.Speeds[3])+float64(CpuAggressive.Speeds[3]))/2 + 0.5)
	if got.Speeds[3] != want {
		t.Fatalf("Speeds[3] = %d, want %d", got.Speeds[3], want)
	}
	if got.Tup != cpuTup {
		t.Fatal("插值必须保留 a 的阈值")
	}
	if !got.Valid() {
		t.Fatal("插值结果必须合法")
	}
}

func TestClampToSafeProtectsTail(t *testing.T) {
	// 构造一条把高温点压得过低的曲线
	low := CpuSilent
	low.Speeds[5] = 10
	low.Speeds[6] = 10
	got := ClampToSafe(low, CpuDefault, 150)
	if got.Speeds[5] < CpuDefault.Speeds[5] || got.Speeds[6] < CpuDefault.Speeds[6] {
		t.Fatalf("尾部安全点未被保护: %+v", got.Speeds)
	}
	// 低温点不受影响
	if got.Speeds[1] != low.Speeds[1] {
		t.Fatal("低温点不应被抬高")
	}
}

func TestClampToSafeCapsMax(t *testing.T) {
	hot := CpuAggressive
	hot.Speeds[6] = 200
	got := ClampToSafe(hot, CpuDefault, 150)
	if got.Speeds[6] != 150 {
		t.Fatalf("超上限未钳制: %d", got.Speeds[6])
	}
}
