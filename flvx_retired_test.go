package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func flvxRetiredTestToken(t *testing.T, a *app) string {
	t.Helper()
	w := flvxTestRequest(a, "POST", "/api/custom/flvx/admin/token", "operator-token", nil)
	var result struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result); result.Token == "" {
		t.Fatalf("token: %d %s", w.Code, w.Body.String())
	}
	return result.Token
}

func flvxRetiredTestList(t *testing.T, a *app, token string) map[int64]map[string]any {
	t.Helper()
	path, auth := "/api/custom/flvx/v1/shares", token
	if token == "" {
		path, auth = "/api/custom/flvx/admin", "operator-token"
	}
	w := flvxTestRequest(a, "GET", path, auth, nil)
	var result struct {
		Shares []map[string]any `json:"shares"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatalf("list %s: %d %s", path, w.Code, w.Body.String())
	}
	items := map[int64]map[string]any{}
	for _, item := range result.Shares {
		hosts, _ := item["hosts"].(map[string]any)
		if item["name"] == "" || hosts == nil || hosts["domain"] == "" && hosts["ipv4"] == "" && hosts["ipv6"] == "" {
			t.Fatalf("broken share item %v", item)
		}
		items[deletionID(item["server_id"])] = item
	}
	return items
}

func flvxRetiredTestCount(t *testing.T, a *app, query string) int {
	t.Helper()
	var n int
	if err := a.flvxDB().QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestServerDeletionRevokesFLVXSharePostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	token := flvxRetiredTestToken(t, a)
	c := flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal(err)
	}
	auditDeleteExec(t, a.flvxDB(), `INSERT INTO mmwxc_flvx_counters VALUES(7,'11','{}'),(7,'12','{}')`)

	// The plan revokes the share before anything else; an unshared server's
	// plan is unchanged.
	data, err := a.adminStore.(*postgresAdminSessionStore).serverDeletionInventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, err := planServerDeletion(data, 7)
	if err != nil || p.Steps[0].Kind != "flvx-share" || p.Steps[0].Name != "撤销 FLVX 共享" {
		t.Fatalf("shared plan steps %+v err=%v", p.Steps, err)
	}
	unshared, err := planServerDeletion(data, 8)
	if err != nil {
		t.Fatal(err)
	}
	delete(data, "mmwxc_flvx_shares")
	before, _ := planServerDeletion(data, 8)
	if unshared.Fingerprint != before.Fingerprint || len(unshared.Steps) != len(before.Steps) {
		t.Fatal("unshared server plan changed")
	}
	for _, step := range unshared.Steps {
		if step.Kind == "flvx-share" {
			t.Fatal("unshared server got a share step")
		}
	}

	for attempt := 0; attempt < 2; attempt++ {
		if err := a.performServerDeletionStep(ctx, "", &p, &p.Steps[0]); err != nil {
			t.Fatal(err)
		}
	}
	s, err := readFLVXShare(ctx, a.flvxDB(), 7, false)
	if err != nil || s.State != "revoked" || s.Deleted == nil || s.Deleted.Name != "shared" || s.Deleted.Hosts.IPv4 != "203.0.113.7" || s.Deleted.SeenAt != 0 {
		t.Fatalf("revoked share %+v %+v err=%v", s, s.Deleted, err)
	}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err == nil {
		t.Fatal("revoked share resumed")
	}

	// The server is gone: FLVX still sees the share, revoked, with the
	// recorded name and hosts. The operator page does not count as seen.
	auditDeleteExec(t, a.flvxDB(), `DELETE FROM remote_servers WHERE id=7; DELETE FROM server_xray_config_snapshots WHERE server_id=7`)
	auditDeleteExec(t, a.flvxDB(), `UPDATE mmwxc_flvx_shares SET data_json=jsonb_set(data_json,'{deleted_server,at}',to_jsonb((extract(epoch from now())*1000)::bigint-25*3600*1000)) WHERE server_id=7`)
	if item := flvxRetiredTestList(t, a, "")[7]; item == nil || item["status"] != "revoked" {
		t.Fatalf("admin list %v", item)
	}
	if s, _ := readFLVXShare(ctx, a.flvxDB(), 7, false); s.Deleted.SeenAt != 0 {
		t.Fatal("operator listing marked the share seen")
	}
	item := flvxRetiredTestList(t, a, token)[7]
	if item == nil || item["status"] != "revoked" || item["name"] != "shared" || item["hosts"].(map[string]any)["ipv4"] != "203.0.113.7" {
		t.Fatalf("FLVX list %v", item)
	}
	if s, _ := readFLVXShare(ctx, a.flvxDB(), 7, false); s.Deleted.SeenAt == 0 {
		t.Fatal("FLVX listing not recorded")
	}
	// Seen and older than 24 h: the next listing drops share and counters.
	if items := flvxRetiredTestList(t, a, token); items[7] != nil {
		t.Fatal("expired share still listed")
	}
	if n := flvxRetiredTestCount(t, a, `SELECT (SELECT count(*) FROM mmwxc_flvx_shares WHERE server_id=7)+(SELECT count(*) FROM mmwxc_flvx_counters WHERE server_id=7)`); n != 0 {
		t.Fatalf("rows left %d", n)
	}
}

func TestFLVXRetiredShareWindowPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	token := flvxRetiredTestToken(t, a)
	c := flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1}
	if err := a.saveFLVXShare(ctx, 7, c, "active", nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	share := func(id int64, deleted *flvxDeletedServer) {
		raw, _ := json.Marshal(flvxShare{Config: c, State: "revoked", Version: 1, Desired: flvx.Desired{Nodes: []flvx.Node{}}, Deleted: deleted})
		auditDeleteExec(t, a.flvxDB(), `INSERT INTO mmwxc_flvx_shares(server_id,data_json) VALUES(`+flvxID(id)+`,'`+string(raw)+`'); INSERT INTO mmwxc_flvx_counters VALUES(`+flvxID(id)+`,'1','{}')`)
	}
	hosts := flvx.Hosts{Domain: "gone.example"}
	share(90, &flvxDeletedServer{Name: "fresh", Hosts: hosts, At: now.Add(-time.Hour).UnixMilli(), SeenAt: now.UnixMilli()})
	share(91, &flvxDeletedServer{Name: "unseen", Hosts: hosts, At: now.Add(-25 * time.Hour).UnixMilli()})
	share(92, &flvxDeletedServer{Name: "abandoned", Hosts: hosts, At: now.Add(-31 * 24 * time.Hour).UnixMilli()})
	share(93, nil)                                                     // deleted outside Custom: nothing recorded
	share(94, &flvxDeletedServer{At: now.Add(-time.Hour).UnixMilli()}) // gone before its name was read

	items := flvxRetiredTestList(t, a, token)
	if items[7] == nil || items[7]["status"] != "active" {
		t.Fatal("live share missing")
	}
	if items[90] == nil || items[91] == nil || items[92] != nil || items[93] != nil || items[94] != nil {
		t.Fatalf("listed %v", items)
	}
	// 90 was seen an hour ago: kept until 24 h. 91 is over 24 h but was seen
	// only now. 92 passed the 30-day cap. 93/94 are skipped, never dropped
	// by the window's first day.
	items = flvxRetiredTestList(t, a, token)
	if items[90] == nil || items[91] != nil {
		t.Fatalf("second listing %v", items)
	}
	got := flvxRetiredTestCount(t, a, `SELECT count(*) FROM mmwxc_flvx_shares`)
	counters := flvxRetiredTestCount(t, a, `SELECT count(*) FROM mmwxc_flvx_counters`)
	if got != 4 || counters != 3 { // 7, 90, 93, 94 (7 has no counters)
		t.Fatalf("shares=%d counters=%d", got, counters)
	}

	// A live server with no address is never sent to FLVX.
	auditDeleteExec(t, a.flvxDB(), `UPDATE remote_servers SET ip_address='' WHERE id=7`)
	if items := flvxRetiredTestList(t, a, token); items[7] != nil || items[90] == nil {
		t.Fatalf("address-less share listed %v", items)
	}
	if !strings.Contains(flvxTestRequest(a, "GET", "/api/custom/flvx/admin", "operator-token", nil).Body.String(), `"server_id":7`) {
		t.Fatal("operator page lost the share")
	}
}
