package main

import (
	"context"
	"testing"
	"time"
)

func TestNextUserRateBaselineUsesActualElapsedAndHandlesReset(t *testing.T) {
	t0 := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	first := nextUserRateBaseline(userRateBaseline{}, userTrafficCounter{Uplink: 100, Downlink: 200, UpdatedAt: t0})
	if first.Valid {
		t.Fatal("first sample must establish a baseline without publishing a rate")
	}
	next := nextUserRateBaseline(first, userTrafficCounter{Uplink: 170, Downlink: 410, UpdatedAt: t0.Add(7 * time.Second)})
	if !next.Valid || next.UploadRate != 10 || next.DownloadRate != 30 {
		t.Fatalf("unexpected non-fixed-interval rate: %#v", next)
	}
	reset := nextUserRateBaseline(next, userTrafficCounter{Uplink: 5, Downlink: 8, UpdatedAt: t0.Add(14 * time.Second)})
	if reset.Valid || reset.UploadRate != 0 || reset.DownloadRate != 0 {
		t.Fatalf("counter reset must only establish a new baseline: %#v", reset)
	}
}

func TestNextUserRateBaselineRejectsStaleGap(t *testing.T) {
	t0 := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	previous := userRateBaseline{Uplink: 10, Downlink: 20, SampleAt: t0}
	next := nextUserRateBaseline(previous, userTrafficCounter{Uplink: 1000, Downlink: 2000, UpdatedAt: t0.Add(userRateMaxSampleGap + time.Second)})
	if next.Valid {
		t.Fatalf("stale gap must not create a rate: %#v", next)
	}
}

func TestExactIdentityOwnersRejectsAmbiguousIdentity(t *testing.T) {
	owners := exactIdentityOwners([]identityOwner{
		{Identity: "shared", Username: "alice"},
		{Identity: "shared", Username: "bob"},
		{Identity: "exact", Username: "alice"},
	})
	if _, exists := owners["shared"]; exists || owners["exact"] != "alice" {
		t.Fatalf("ambiguous identity was not rejected: %#v", owners)
	}
}

func TestNormalizedUserRatesAggregatesFreshExternalServers(t *testing.T) {
	t0 := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	app := &app{userRateBaselines: make(map[string]userRateBaseline), userRateOwnership: make(map[string]userRateOwnershipCacheEntry)}
	records := map[string]serverDetailedConnectionRecord{
		"5":  {UpdatedAt: t0, Snapshot: serverDetailedConnectionSnapshot{ProxyUsers: []serverProxyUserConnections{{Identity: serverConnectionIdentity{User: "id-a"}, ManagementGroup: "ken"}}}},
		"14": {UpdatedAt: t0, Snapshot: serverDetailedConnectionSnapshot{ProxyUsers: []serverProxyUserConnections{{Identity: serverConnectionIdentity{User: "id-b"}, ManagementGroup: "ken"}}}},
	}
	modes := map[string]string{"5": "external", "14": "external"}
	first := []userTrafficCounter{
		{ServerID: "5", Identity: "id-a", Uplink: 100, Downlink: 200, UpdatedAt: t0},
		{ServerID: "14", Identity: "id-b", Uplink: 300, Downlink: 400, UpdatedAt: t0},
	}
	if rates := app.normalizedUserRates(context.Background(), t0, first, records, modes); len(rates) != 0 {
		t.Fatalf("first sample published a rate: %#v", rates)
	}
	secondAt := t0.Add(10 * time.Second)
	records["5"] = serverDetailedConnectionRecord{UpdatedAt: secondAt, Snapshot: records["5"].Snapshot}
	records["14"] = serverDetailedConnectionRecord{UpdatedAt: secondAt, Snapshot: records["14"].Snapshot}
	second := []userTrafficCounter{
		{ServerID: "5", Identity: "id-a", Uplink: 200, Downlink: 400, UpdatedAt: secondAt},
		{ServerID: "14", Identity: "id-b", Uplink: 500, Downlink: 700, UpdatedAt: secondAt},
	}
	rates := app.normalizedUserRates(context.Background(), secondAt, second, records, modes)
	ken := rates["ken"]
	if !ken.Fresh || ken.Upload != 30 || ken.Download != 50 || ken.Total != 80 || len(ken.Sources) != 2 {
		t.Fatalf("unexpected multi-server aggregate: %#v", ken)
	}
}

func TestAdvanceUserRateBaselinesDropsExpiredRate(t *testing.T) {
	t0 := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	app := &app{userRateBaselines: map[string]userRateBaseline{
		"5\x00id": {Uplink: 10, Downlink: 20, SampleAt: t0, UploadRate: 1, DownloadRate: 2, RateAt: t0, Valid: true},
	}}
	counters := []userTrafficCounter{{ServerID: "5", Identity: "id", Uplink: 10, Downlink: 20, UpdatedAt: t0}}
	if current := app.advanceUserRateBaselines(t0.Add(userRateStaleTimeout+time.Second), counters); len(current) != 0 {
		t.Fatalf("expired rate remained visible: %#v", current)
	}
}
