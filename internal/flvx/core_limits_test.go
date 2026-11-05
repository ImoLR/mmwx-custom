package flvx

import "testing"

func TestCoreLimitsUnlimited(t *testing.T) {
	base := Config{PortMin: 30000, PortMax: 30999, ResetDay: 1}
	for _, ok := range [][2]int{{0, 0}, {0, 50}, {256, 0}, {64, 1}, {4096, 400}} {
		c := base
		c.MemoryMB, c.CPUPercent = ok[0], ok[1]
		if err := ValidateConfig(c); err != nil {
			t.Errorf("%v rejected: %v", ok, err)
		}
	}
	for _, bad := range [][2]int{{1, 0}, {63, 50}, {4097, 50}, {-1, 0}, {0, -1}, {0, 401}} {
		c := base
		c.MemoryMB, c.CPUPercent = bad[0], bad[1]
		if ValidateConfig(c) == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	for in, want := range map[[2]int][2]int{{0, 0}: {4096, 400}, {0, 50}: {4096, 50}, {128, 0}: {128, 400}, {256, 50}: {256, 50}} {
		c := base
		c.MemoryMB, c.CPUPercent = in[0], in[1]
		got := LegacyCoreLimits(c)
		if got.MemoryMB != want[0] || got.CPUPercent != want[1] || got.PortMin != c.PortMin {
			t.Errorf("%v -> %d / %d", in, got.MemoryMB, got.CPUPercent)
		}
	}
}
