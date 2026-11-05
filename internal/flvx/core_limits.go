package flvx

// 共享资源上限 「不限制」 (user 2026-11-06): Config.MemoryMB / CPUPercent 0
// mean no MemoryMax / CPUQuota on the FLVX Core instance. Helpers with
// flvx.core_unlimited.v1 apply 0 as no limit; older ones reject 0 (it was
// never valid there), so Custom hands them the largest limits they accept.
// Only the FLVX Core instance is involved; mmwx's own Core never is.
const (
	CoreUnlimitedCapability = "flvx.core_unlimited.v1"

	MinCoreMemoryMB   = 64
	MaxCoreMemoryMB   = 4096
	MaxCoreCPUPercent = 400
)

// LegacyCoreLimits is c for a Helper without flvx.core_unlimited.v1: 不限制
// becomes the largest limits it accepts (4096 MB / 400 %), never a small cap.
func LegacyCoreLimits(c Config) Config {
	if c.MemoryMB == 0 {
		c.MemoryMB = MaxCoreMemoryMB
	}
	if c.CPUPercent == 0 {
		c.CPUPercent = MaxCoreCPUPercent
	}
	return c
}
