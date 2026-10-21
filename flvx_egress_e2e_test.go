package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// TestFLVXEgressE2EWithFLVXClient drives Custom's real FLVX v1 HTTP API with
// a driver built from FLVX's own go-backend (preset rules, profile normalizer,
// plan compiler and landingcustom HTTP client; MMWXC_TEST_FLVX_E2E_BIN). The
// signed Helper envelope it produces is written to MMWXC_TEST_EVIDENCE for
// TestFLVXEgressE2EEnvelopeRealCore (Helper side, real Core).
func TestFLVXEgressE2EWithFLVXClient(t *testing.T) {
	driver := os.Getenv("MMWXC_TEST_FLVX_E2E_BIN")
	if driver == "" {
		t.Skip("MMWXC_TEST_FLVX_E2E_BIN not configured")
	}
	a := flvxTestApp(t)
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/custom/flvx/v1/", a.flvxV1Handler)
	server := httptest.NewServer(mux)
	defer server.Close()
	w := flvxTestRequest(a, "POST", "/api/custom/flvx/admin/token", "operator-token", nil)
	var tokenResult struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &tokenResult)
	if err := a.saveFLVXShare(ctx, 7, flvx.Config{PortMin: 30000, PortMax: 30100, ResetDay: 1, MemoryMB: 256, CPUPercent: 50}, "active", nil); err != nil {
		t.Fatal(err)
	}
	// FLVX's first push before any profile exists: today's payload.
	if err := a.putFLVXDesired(ctx, 7, flvxTestDesired()); err != nil {
		t.Fatal(err)
	}
	evidence := os.Getenv("MMWXC_TEST_EVIDENCE")
	if evidence == "" {
		evidence = t.TempDir()
	}
	steps := []map[string]any{}
	run := func(args ...string) map[string]any {
		t.Helper()
		cmd := exec.Command(driver, append([]string{server.URL, tokenResult.Token}, args...)...)
		cmd.Env = append(os.Environ(), "MMWXE2E_PLAN="+filepath.Join(evidence, "e2e-flvx-desired.json"))
		out, err := cmd.Output()
		var result map[string]any
		if err != nil || json.Unmarshal(out, &result) != nil {
			t.Fatalf("FLVX driver %v: %v %s", args, err, out)
		}
		steps = append(steps, result)
		return result
	}
	// 1. Helper without egress support: FLVX sees no capability; a plan sent
	// anyway is refused with 422.
	flvxTestReport(t, a, 1, nil)
	if r := run("shares"); r["supports_v1"] != false || r["egress"] != nil {
		t.Fatalf("old Helper advertised: %v", r)
	}
	if r := run("desired", "2"); r["status"] != float64(422) {
		t.Fatalf("plan for an old Helper: %v", r)
	}
	// 2. New Helper: FLVX compiles all presets (屏蔽中国大陆 incl. 禁止大陆直连
	// with exempt FLVX nodes, 防止送中, 测速, AI, TikTok, 流媒体, BT, 广告) for
	// two profiles and Custom accepts the plan.
	flvxTestReport(t, a, 2, &flvx.EgressStatus{Versions: []int{1}, GeoIP: true, GeoSite: true, State: "none", AppliedRevision: 0})
	if r := run("shares"); r["supports_v1"] != true || r["allowed"] != true {
		t.Fatalf("capability: %v", r)
	}
	r := run("desired", "2")
	if r["error"] != "<nil>" || r["rules"].(float64) < 12 || r["outbounds"] != float64(4) {
		t.Fatalf("FLVX plan refused: %v", r)
	}
	s, _ := readFLVXShare(ctx, a.flvxDB(), 7, false)
	if !s.Desired.Egress.Active() || len(s.Desired.Egress.ExemptSources) == 0 {
		t.Fatalf("plan not stored: %+v", s.Desired.Egress)
	}
	e, err := a.flvxHelperDesired(ctx, 7, "7", "helper-e2e-token", "amd64", true, false, nil)
	if err != nil || e.Payload.Desired.Egress == nil {
		t.Fatal("Helper envelope", err)
	}
	raw, _ := json.Marshal(e)
	os.WriteFile(filepath.Join(evidence, "e2e-envelope.json"), raw, 0600)
	// 3. The Helper's state comes back to FLVX through the status endpoint.
	flvxTestReport(t, a, 3, &flvx.EgressStatus{Versions: []int{1}, GeoIP: true, GeoSite: true, State: "applied", AppliedRevision: 2, Warnings: []string{"flvxo-6: 服务器没有可用的 IPv6 地址，强制 IPv6 的连接会失败"}})
	st := run("status")["egress"].(map[string]any)
	if st["state"] != "applied" || st["applied_revision"] != float64(2) || len(st["warnings"].([]any)) != 1 {
		t.Fatalf("status: %v", st)
	}
	// 4. Invalid upstream from FLVX: 422.
	if r := run("desired-invalid", "3"); r["status"] != float64(422) {
		t.Fatalf("private upstream: %v", r)
	}
	// 5. Policy off: FLVX sees allowed:false; plans are refused.
	if w := flvxTestRequest(a, "PUT", "/api/custom/flvx/admin/shares/7", "operator-token", map[string]any{"status": "active", "allow_egress": false}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if r := run("shares"); r["allowed"] != false || r["supports_v1"] != true {
		t.Fatalf("policy off: %v", r)
	}
	if r := run("desired", "3"); r["status"] != float64(422) {
		t.Fatalf("plan with policy off: %v", r)
	}
	report, _ := json.MarshalIndent(steps, "", "  ")
	os.WriteFile(filepath.Join(evidence, "e2e-flvx-client.json"), report, 0644)
	t.Logf("FLVX client e2e: %d steps; plan %v rules / %v outbounds / %v exempt sources, %v bytes", len(steps), r["rules"], r["outbounds"], r["exempt"], r["bytes"])
	if !strings.Contains(string(report), "422") {
		t.Fatal("expected refusals missing")
	}
}
