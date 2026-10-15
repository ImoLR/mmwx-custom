package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type inactiveServiceCache struct {
	mu    sync.Mutex
	until time.Time
}

var inactiveServices sync.Map

func serviceCache(service string) *inactiveServiceCache {
	cache, _ := inactiveServices.LoadOrStore(service, &inactiveServiceCache{})
	return cache.(*inactiveServiceCache)
}

func invalidateServiceCaches() {
	inactiveServices.Range(func(_, value any) bool {
		value.(*inactiveServiceCache).invalidate()
		return true
	})
}

func (cache *inactiveServiceCache) active(now time.Time, cgroupPresent bool, probe func() bool) bool {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if !cgroupPresent && now.Before(cache.until) {
		return false
	}
	active := probe()
	cache.until = time.Time{}
	if !active {
		cache.until = now.Add(time.Minute)
	}
	return active
}

func (cache *inactiveServiceCache) invalidate() {
	cache.mu.Lock()
	cache.until = time.Time{}
	cache.mu.Unlock()
}

func cachedServiceActive(ctx context.Context, service string) bool {
	// A service started outside the Helper creates its cgroup before becoming
	// active. Check both unified and legacy systemd hierarchies to bypass the
	// negative cache immediately; unsupported layouts still refresh within 60s.
	cgroupPresent := false
	for _, root := range []string{"/sys/fs/cgroup", "/sys/fs/cgroup/systemd"} {
		if _, err := os.Stat(filepath.Join(root, "system.slice", service)); err == nil {
			cgroupPresent = true
		}
	}
	return probeServiceActive(ctx, service, time.Now(), cgroupPresent)
}

func probeServiceActive(ctx context.Context, service string, now time.Time, cgroupPresent bool) bool {
	return serviceCache(service).active(now, cgroupPresent, func() bool {
		if cgroupPresent {
			return systemctl(ctx, "is-active", "--quiet", service) == nil
		}
		// Unlike is-active/show, list-units only queries already loaded units.
		// Even a cache refresh must not load a stopped unit and repeat PID 1's
		// parser warnings (e.g. the official unit's User=nobody).
		output, err := exec.CommandContext(ctx, "systemctl", "list-units", "--all", "--plain", "--no-legend", "--no-pager", service).Output()
		if err != nil {
			return false
		}
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 3 && fields[0] == service {
				return fields[2] == "active" || fields[2] == "reloading"
			}
		}
		return false
	})
}
