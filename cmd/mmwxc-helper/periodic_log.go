package main

import (
	"log"
	"regexp"
	"time"
)

const periodicLogInterval = 10 * time.Minute

var (
	logTimestamp = regexp.MustCompile(`\b\d{4}[-/]\d{2}[-/]\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?`)
	logPort      = regexp.MustCompile(`([a-zA-Z0-9_.-]+|\[[0-9a-fA-F:%]+\]):\d{1,5}\b`)
)

type periodicLogSite struct {
	seen     map[string]bool
	since    time.Time
	repeated uint64
}

// Only periodic sites use this limiter; command and startup logs stay immediate.
type periodicLogLimiter struct {
	sites map[string]*periodicLogSite
	now   func() time.Time
	print func(string, ...any)
}

func newPeriodicLogLimiter() *periodicLogLimiter {
	return &periodicLogLimiter{sites: make(map[string]*periodicLogSite), now: time.Now, print: log.Printf}
}

func (limiter *periodicLogLimiter) report(site string, err error) {
	if err == nil {
		if state := limiter.sites[site]; state != nil {
			limiter.print("[mmwxc-helper] %s: recovered (%d repeats suppressed)", site, state.repeated)
			delete(limiter.sites, site)
		}
		return
	}
	now := limiter.now()
	message := logPort.ReplaceAllString(logTimestamp.ReplaceAllString(err.Error(), "<time>"), "${1}:<port>")
	state := limiter.sites[site]
	if state == nil {
		state = &periodicLogSite{seen: make(map[string]bool), since: now}
		limiter.sites[site] = state
	}
	if state.seen[message] {
		state.repeated++
	} else {
		limiter.print("[mmwxc-helper] %s: %v", site, err)
		// Bound memory even if a remote endpoint returns arbitrarily many errors.
		if len(state.seen) >= 64 {
			for key := range state.seen {
				delete(state.seen, key)
				break
			}
		}
		state.seen[message] = true
	}
	if now.Sub(state.since) >= periodicLogInterval {
		if state.repeated > 0 {
			limiter.print("[mmwxc-helper] %s: repeated %d times in the last 10m", site, state.repeated)
		}
		state.since, state.repeated = now, 0
	}
}
