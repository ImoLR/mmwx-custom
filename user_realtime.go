package main

import (
	"context"
	"sort"
	"strings"
	"time"
)

const (
	userRateStaleTimeout = 2 * time.Minute
	userRateMaxSampleGap = 3 * time.Minute
	userRateOwnershipTTL = 30 * time.Second
)

type userRateBaseline struct {
	Uplink       int64
	Downlink     int64
	SampleAt     time.Time
	UploadRate   int64
	DownloadRate int64
	RateAt       time.Time
	Valid        bool
}

type userRateOwnershipCacheEntry struct {
	Owners    map[string]string
	ExpiresAt time.Time
}

type normalizedUserRateSource struct {
	ServerID  string `json:"server_id"`
	Identity  string `json:"identity"`
	Mode      string `json:"mode"`
	Upload    int64  `json:"upload_bytes_per_second"`
	Download  int64  `json:"download_bytes_per_second"`
	UpdatedAt string `json:"rate_updated_at"`
}

type normalizedUserRate struct {
	Upload    int64                      `json:"upload_bytes_per_second"`
	Download  int64                      `json:"download_bytes_per_second"`
	Total     int64                      `json:"total_bytes_per_second"`
	UpdatedAt string                     `json:"rate_updated_at,omitempty"`
	Fresh     bool                       `json:"rate_fresh"`
	Sources   []normalizedUserRateSource `json:"sources"`
}

type identityOwner struct {
	Identity string
	Username string
}

func exactIdentityOwners(values []identityOwner) map[string]string {
	owners := make(map[string]string)
	conflicts := make(map[string]bool)
	for _, value := range values {
		identity := strings.TrimSpace(value.Identity)
		username := strings.TrimSpace(value.Username)
		if identity == "" || username == "" || conflicts[identity] {
			continue
		}
		if current, exists := owners[identity]; exists && current != username {
			delete(owners, identity)
			conflicts[identity] = true
			continue
		}
		owners[identity] = username
	}
	return owners
}

func externalIdentityOwners(record serverDetailedConnectionRecord) map[string]string {
	values := make([]identityOwner, 0, len(record.Snapshot.ProxyUsers))
	for _, runtime := range record.Snapshot.ProxyUsers {
		values = append(values, identityOwner{Identity: runtime.Identity.User, Username: runtime.ManagementGroup})
	}
	return exactIdentityOwners(values)
}

func (a *app) embeddedIdentityOwners(ctx context.Context, serverID string, now time.Time) map[string]string {
	a.userRateMu.Lock()
	cached, exists := a.userRateOwnership[serverID]
	if exists && now.Before(cached.ExpiresAt) {
		result := cached.Owners
		a.userRateMu.Unlock()
		return result
	}
	a.userRateMu.Unlock()

	ownership, err := a.connectionOwnership(ctx, serverID)
	if err != nil {
		return map[string]string{}
	}
	values := make([]identityOwner, 0, len(ownership.Relations))
	for _, relation := range ownership.Relations {
		values = append(values, identityOwner{Identity: relation.ProtocolIdentity, Username: relation.ManagementUsername})
	}
	owners := exactIdentityOwners(values)
	a.userRateMu.Lock()
	a.userRateOwnership[serverID] = userRateOwnershipCacheEntry{Owners: owners, ExpiresAt: now.Add(userRateOwnershipTTL)}
	a.userRateMu.Unlock()
	return owners
}

func nextUserRateBaseline(previous userRateBaseline, counter userTrafficCounter) userRateBaseline {
	next := userRateBaseline{Uplink: counter.Uplink, Downlink: counter.Downlink, SampleAt: counter.UpdatedAt}
	if previous.SampleAt.IsZero() || !counter.UpdatedAt.After(previous.SampleAt) {
		if counter.UpdatedAt.Equal(previous.SampleAt) {
			return previous
		}
		return next
	}
	elapsed := counter.UpdatedAt.Sub(previous.SampleAt)
	if elapsed < 250*time.Millisecond || elapsed > userRateMaxSampleGap || counter.Uplink < previous.Uplink || counter.Downlink < previous.Downlink {
		return next
	}
	seconds := elapsed.Seconds()
	next.UploadRate = int64(float64(counter.Uplink-previous.Uplink) / seconds)
	next.DownloadRate = int64(float64(counter.Downlink-previous.Downlink) / seconds)
	next.RateAt = counter.UpdatedAt
	next.Valid = true
	return next
}

func (a *app) advanceUserRateBaselines(now time.Time, counters []userTrafficCounter) map[string]userRateBaseline {
	a.userRateMu.Lock()
	defer a.userRateMu.Unlock()
	result := make(map[string]userRateBaseline, len(counters))
	for _, counter := range counters {
		key := counter.ServerID + "\x00" + counter.Identity
		next := nextUserRateBaseline(a.userRateBaselines[key], counter)
		a.userRateBaselines[key] = next
		if next.Valid && !next.RateAt.After(now.Add(5*time.Second)) && now.Sub(next.RateAt) <= userRateStaleTimeout {
			result[key] = next
		}
	}
	return result
}

func (a *app) normalizedUserRates(ctx context.Context, now time.Time, counters []userTrafficCounter, records map[string]serverDetailedConnectionRecord, modes map[string]string) map[string]normalizedUserRate {
	baselines := a.advanceUserRateBaselines(now, counters)
	ownersByServer := make(map[string]map[string]string)
	for serverID, mode := range modes {
		if mode == "external" {
			record, exists := records[serverID]
			if !exists || record.UpdatedAt.IsZero() || now.Sub(record.UpdatedAt) > helperStaleTimeout {
				continue
			}
			ownersByServer[serverID] = externalIdentityOwners(record)
			continue
		}
		if mode == "embedded" {
			ownersByServer[serverID] = a.embeddedIdentityOwners(ctx, serverID, now)
		}
	}

	rates := make(map[string]normalizedUserRate)
	latestByUser := make(map[string]time.Time)
	for _, counter := range counters {
		username := ownersByServer[counter.ServerID][counter.Identity]
		if username == "" {
			continue
		}
		baseline, valid := baselines[counter.ServerID+"\x00"+counter.Identity]
		if !valid {
			continue
		}
		rate := rates[username]
		rate.Upload += baseline.UploadRate
		rate.Download += baseline.DownloadRate
		rate.Total = rate.Upload + rate.Download
		rate.Fresh = true
		if baseline.RateAt.After(latestByUser[username]) {
			latestByUser[username] = baseline.RateAt
			rate.UpdatedAt = baseline.RateAt.Format(time.RFC3339Nano)
		}
		rate.Sources = append(rate.Sources, normalizedUserRateSource{
			ServerID: counter.ServerID, Identity: counter.Identity, Mode: modes[counter.ServerID],
			Upload: baseline.UploadRate, Download: baseline.DownloadRate, UpdatedAt: baseline.RateAt.Format(time.RFC3339Nano),
		})
		rates[username] = rate
	}
	for username, rate := range rates {
		sort.Slice(rate.Sources, func(i, j int) bool {
			if rate.Sources[i].ServerID != rate.Sources[j].ServerID {
				return rate.Sources[i].ServerID < rate.Sources[j].ServerID
			}
			return rate.Sources[i].Identity < rate.Sources[j].Identity
		})
		rates[username] = rate
	}
	return rates
}
