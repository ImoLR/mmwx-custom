package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type inactiveServiceCache struct {
	mu    sync.Mutex
	until time.Time
}

var inactiveCoreService inactiveServiceCache

func (cache *inactiveServiceCache) active(now time.Time, cgroupPresent bool, probe func() bool) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if !cgroupPresent && now.Before(cache.until) {
		return false
	}
	active := probe()
	cache.until = time.Time{}
	if !active {
		cache.until = now.Add(30 * time.Second)
	}
	return active
}

func (cache *inactiveServiceCache) invalidate() {
	cache.mu.Lock()
	cache.until = time.Time{}
	cache.mu.Unlock()
}

func coreServiceActive(ctx context.Context) bool {
	// A service started outside the Helper creates its cgroup before becoming
	// active. Check both unified and legacy systemd hierarchies to bypass the
	// negative cache immediately; unsupported layouts still refresh within 30s.
	cgroupPresent := false
	for _, root := range []string{"/sys/fs/cgroup", "/sys/fs/cgroup/systemd"} {
		if _, err := os.Stat(filepath.Join(root, "system.slice", coreServiceName)); err == nil {
			cgroupPresent = true
		}
	}
	return inactiveCoreService.active(time.Now(), cgroupPresent, func() bool {
		return systemctl(ctx, "is-active", "--quiet", coreServiceName) == nil
	})
}
