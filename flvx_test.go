package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

func flvxTestApp(t *testing.T) *app {
	db := auditUserDeleteDB(t, "flvx")
	for _, s := range []string{flvxSchema, `ALTER TABLE remote_servers ADD COLUMN ip_address text,ADD COLUMN domain text`,
		`CREATE TABLE server_xray_config_snapshots(id bigint,server_id bigint,config_json text,status text,created_at timestamp)`,
		`INSERT INTO remote_servers VALUES(7,'shared','203.0.113.7',''),(8,'native','203.0.113.8','')`,
		`INSERT INTO server_xray_config_snapshots VALUES(1,7,'{"inbounds":[{"port":30002}]}','current',CURRENT_TIMESTAMP),(2,8,'{"inbounds":[]}','current',CURRENT_TIMESTAMP)`,
	} {
		auditDeleteExec(t, db, s)
	}
	return &app{apiToken: "operator-token", adminStore: &postgresAdminSessionStore{db: db}}
}

func flvxTestRequest(a *app, method, path, token string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	if strings.Contains(path, "/admin") {
		a.flvxAdminHandler(w, r)
	} else {
		a.flvxV1Handler(w, r)
	}
	return w
}

func flvxTestDesired() flvx.Desired {
	enabled := true
	key := base64.StdEncoding.EncodeToString(make([]byte, 16))
	return flvx.Desired{Revision: 1, Nodes: []flvx.Node{{ID: "11", Owner: flvx.Owner{ID: "2", Name: "Alice"}, Name: "SS", Protocol: "ss2022", Port: 30001, Enabled: &enabled, SS2022: &flvx.SS2022{Method: "2022-blake3-aes-128-gcm", ServerKey: key, UserKey: key}}}}
}

func TestFLVXAPIContractPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	w := flvxTestRequest(a, "POST", "/api/custom/flvx/admin/token", "operator-token", nil)
	var tokenResult struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &tokenResult)
	token := tokenResult.Token
	if w.Code != 200 || token == "" {
		t.Fatalf("token: %d %s", w.Code, w.Body.String())
	}
	var hash string
	a.flvxDB().QueryRow(`SELECT token_hash FROM mmwxc_flvx_integration`).Scan(&hash)
	if hash == token || hash != flvxTokenHash(token) {
		t.Fatal("token not hashed")
	}
	for _, wrong := range []string{"operator-token", "helper-token", "", "flvx_wrong"} {
		if w := flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares", wrong, nil); w.Code != 401 {
			t.Fatalf("wrong token accepted: %d", w.Code)
		}
	}
	if w := flvxTestRequest(a, "GET", "/api/custom/flvx/admin", token, nil); w.Code != 401 {
		t.Fatal("integration token authorized as operator")
	}
	c := flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50, TrafficLimitBytes: 100, MaxConnections: 10}
	if err := a.saveFLVXShare(ctx, 7, c, "active"); err != nil {
		t.Fatal(err)
	}
	d := flvxTestDesired()
	w = flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", token, d)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"accepted_revision":1}` {
		t.Fatalf("desired %d %s", w.Code, w.Body.String())
	}
	if err := a.putFLVXDesired(ctx, 7, d); err != nil {
		t.Fatal("idempotent retry", err)
	}
	d.Nodes[0].Port = 30002
	d.Revision = 2
	w = flvxTestRequest(a, "PUT", "/api/custom/flvx/v1/shares/7/desired", token, d)
	if w.Code != 422 || !strings.Contains(w.Body.String(), `"field":"port"`) {
		t.Fatalf("collision %d %s", w.Code, w.Body.String())
	}
	d = flvxTestDesired()
	d.Nodes[0].Name = "different"
	if err := a.putFLVXDesired(ctx, 7, d); err == nil {
		t.Fatal("same revision changed")
	}
	w = flvxTestRequest(a, "GET", "/api/custom/flvx/admin", "operator-token", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "server_key") || strings.Contains(w.Body.String(), token) {
		t.Fatal("admin credentials leaked")
	}
	for _, state := range []string{"paused", "active", "revoked"} {
		if err := a.saveFLVXShare(ctx, 7, c, state); err != nil {
			t.Fatal(err)
		}
		err := a.putFLVXDesired(ctx, 7, flvxTestDesired())
		if (state == "active") != (err == nil) {
			t.Fatalf("state %s: %v", state, err)
		}
	}
	w = flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares/7/status", token, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"nodes":[]`) {
		t.Fatalf("status %s", w.Body.String())
	}
	w = flvxTestRequest(a, "DELETE", "/api/custom/flvx/admin/token", "operator-token", nil)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if flvxTestRequest(a, "GET", "/api/custom/flvx/v1/shares", token, nil).Code != 401 {
		t.Fatal("revoked token works")
	}
}

func TestFLVXCountersPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	c := flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}
	if err := a.saveFLVXShare(ctx, 7, c, "active"); err != nil {
		t.Fatal(err)
	}
	if err := a.putFLVXDesired(ctx, 7, flvxTestDesired()); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		generation    string
		seq, up, want int64
	}{{"a", 1, 100, 100}, {"a", 1, 100, 100}, {"a", 2, 150, 150}, {"b", 1, 10, 160}, {"a", 3, 200, 160}, {"b", 2, 1, 161}} {
		err := flvxTransaction(ctx, a.flvxDB(), func(tx *sql.Tx) error {
			s, e := readFLVXShare(ctx, tx, 7, true)
			if e != nil {
				return e
			}
			r := flvx.Report{Status: flvx.Status{AppliedRevision: 1, Core: flvx.Core{State: "running", Epoch: v.generation}, Nodes: []flvx.NodeStatus{{ID: "11", State: "active", UpBytes: v.up}}, CollectedAt: time.Now().UnixMilli()}, Generation: v.generation, Sequence: v.seq, Period: flvx.Period(time.Now(), 1)}
			if e = mergeFLVXReport(ctx, tx, 7, &s, r, time.Now()); e != nil {
				return e
			}
			return writeFLVXShare(ctx, tx, 7, s)
		})
		if err != nil {
			t.Fatal(err)
		}
		s, err := readFLVXShare(ctx, a.flvxDB(), 7, false)
		if err != nil || len(s.Status.Nodes) != 1 || s.Status.Nodes[0].UpBytes != v.want || s.Status.TrafficUsedBytes != v.want {
			t.Fatalf("merge %v: %+v %v", v, s, err)
		}
	}
}

func TestFLVXDualInstallPostgres(t *testing.T) {
	a := flvxTestApp(t)
	ctx := context.Background()
	h := flvx.LandingHosts{Native: []string{"203.0.113.8"}, Nodes: []string{"203.0.113.7"}}
	if err := a.putFLVXHosts(ctx, h); err != nil {
		t.Fatal(err)
	}
	c := flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}
	if err := a.saveFLVXShare(ctx, 7, c, "active"); err != nil {
		t.Fatal("dual install must warn only", err)
	}
	if err := a.saveFLVXShare(ctx, 8, c, "active"); err == nil {
		t.Fatal("native landing accepted")
	}
	h.Native = append(h.Native, "203.0.113.7")
	if err := a.putFLVXHosts(ctx, h); err != nil {
		t.Fatal(err)
	}
	s, _ := readFLVXShare(ctx, a.flvxDB(), 7, false)
	if s.State != "paused" {
		t.Fatal("late native conflict not paused")
	}
	server := flvxServer{Addresses: []string{"shared.example"}}
	detectFLVXDual(ctx, &server, flvx.LandingHosts{Native: []string{"203.0.113.7"}}, func(context.Context, string) ([]string, error) { return []string{"203.0.113.7"}, nil })
	if !server.NativeLanding {
		t.Fatal("hostname resolution not compared")
	}
}
