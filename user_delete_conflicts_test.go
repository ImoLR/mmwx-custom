package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestDeleteUserConflictsRefuseNewOperationButAllowPartialRetry(t *testing.T) {
	for _, retry := range []bool{false, true} {
		name := "new"
		if retry {
			name = "partial_retry"
		}
		t.Run(name, func(t *testing.T) {
			alice := map[string]any{"id": "alice-secret", "email": "alice__safe"}
			conflicted := map[string]any{"id": "alice-conflicted", "email": "alice__conflicted"}
			store := &lifecycleTestStore{refs: []lifecycleCredentialRef{
				lifecycleRef(5, "safe", "vless", alice),
				lifecycleRef(5, "conflicted", "vless", conflicted),
			}, packages: []lifecyclePackageBinding{{ID: 11, Name: "冲突套餐", Bound: true}}, finishedPending: -1}
			if retry {
				store.operationID = "existing-partial-delete"
			}
			fixture, server := newLifecycleAgentFixture(map[int64]map[string]any{5: lifecycleConfig(
				lifecycleInbound("safe", "vless", alice),
				lifecycleInbound("conflicted", "vless", conflicted),
			)})
			defer server.Close()
			application := lifecycleTestApp(t, store, server)
			request := func(method, path string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, "/api/custom/users/alice/"+path, nil)
				r.Header.Set("MM-Authorization", "admin-session")
				response := httptest.NewRecorder()
				application.userManagementHandler(response, r)
				return response
			}
			preview := request(http.MethodGet, "deletion-preview")
			if preview.Code != http.StatusOK || strings.Contains(preview.Body.String(), `"action":"CONFLICT"`) {
				t.Fatalf("initial preview: status=%d body=%s", preview.Code, preview.Body.String())
			}
			// Conflicts arise after preview; the POST must use a fresh plan.
			fixture.configs[5] = lifecycleConfig(lifecycleInbound("safe", "vless", alice),
				lifecycleInbound("conflicted", "vless", conflicted, map[string]any{"id": "unknown-secret", "email": "unknown"}))
			store.packages[0].BindingConflict = true
			before, _ := json.Marshal(fixture.configs)
			response := request(http.MethodPost, "delete")
			if !retry {
				if response.Code != http.StatusConflict {
					t.Fatalf("new deletion must refuse conflicts: status=%d body=%s", response.Code, response.Body.String())
				}
				var body struct {
					Success bool   `json:"success"`
					Message string `json:"message"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				for _, fragment := range []string{"存在冲突，删除不会执行，请先处理以下项目", "服务器 server-5（ID 5）入站 conflicted", "无法确认来源", "套餐 冲突套餐（ID 11）", "套餐同时绑定其他用户"} {
					if !strings.Contains(body.Message, fragment) {
						t.Fatalf("missing conflict detail %q: %s", fragment, body.Message)
					}
				}
				after, _ := json.Marshal(fixture.configs)
				if body.Success || len(fixture.actions) != 0 || !reflect.DeepEqual(before, after) ||
					store.operationID != "" || len(store.savedPlans) != 0 || len(store.marked) != 0 ||
					len(store.deletedPackages) != 0 || store.finalized || store.finishedPending != -1 {
					t.Fatalf("refused deletion changed runtime or persistence: actions=%+v store=%+v", fixture.actions, store)
				}
				return
			}
			var body struct {
				Success bool                  `json:"success"`
				Result  lifecycleDeleteResult `json:"result"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || !body.Success || body.Result.State != lifecycleStateDeletePartial ||
				body.Result.OperationID != "existing-partial-delete" || body.Result.UserDeleted || body.Result.PendingCount != 2 {
				t.Fatalf("partial retry must keep existing behavior: status=%d body=%s", response.Code, response.Body.String())
			}
			if len(fixture.actions) != 1 || fixture.actions[0]["tag"] != "safe" || fixture.actions[0]["action"] != "remove" ||
				findConfigInbound(fixture.configs[5], "safe") != nil || findConfigInbound(fixture.configs[5], "conflicted") == nil ||
				len(store.savedPlans) != 1 || len(store.marked) != 2 || store.finishedPending != 2 || store.finalized || len(store.deletedPackages) != 0 {
				t.Fatalf("partial retry did not clean only safe items: actions=%+v store=%+v", fixture.actions, store)
			}
		})
	}
}
