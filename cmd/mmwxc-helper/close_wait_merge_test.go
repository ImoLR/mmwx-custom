package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// The Custom half (close_wait_merge_test.go at the repository root) records
// what Custom sends before and after the two server CLOSE_WAIT inputs were
// merged into one; this half runs those payloads through the real Helper.
func TestCloseWaitMergeKeepsEffectiveValue(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/close-wait-merge.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name           string          `json:"name"`
		MachineV2      bool            `json:"machine_v2"`
		Before         json.RawMessage `json:"before"`
		After          json.RawMessage `json:"after"`
		WantCoreBefore *int64          `json:"want_core_before"`
		WantCoreAfter  *int64          `json:"want_core_after"`
		WantFLVXAfter  *int64          `json:"want_flvx_after"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	decode := func(name string, payload json.RawMessage) connectionSettings {
		var settings connectionSettings
		if err := json.Unmarshal(payload, &settings); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return settings
	}
	checked := 0
	for _, item := range cases {
		// v1 payloads are for older Helpers; Custom's half checks those.
		if !item.MachineV2 {
			continue
		}
		checked++
		before, after := decode(item.Name, item.Before), decode(item.Name, item.After)
		if got := before.coreConfig().DefaultCloseWaitTimeoutSeconds; !reflect.DeepEqual(got, item.WantCoreBefore) {
			t.Fatalf("%s: mmwx Core before = %v, want %v", item.Name, got, item.WantCoreBefore)
		}
		if got := after.coreConfig().DefaultCloseWaitTimeoutSeconds; !reflect.DeepEqual(got, item.WantCoreAfter) {
			t.Fatalf("%s: mmwx Core after = %v, want %v", item.Name, got, item.WantCoreAfter)
		}
		// The FLVX shared Core and the status file's close_wait_seconds both
		// take machine protection's value.
		shared := &flvxManager{}
		shared.setMachineCloseWait(after.MachineProtection)
		if got := shared.machineCloseWaitSeconds(); !reflect.DeepEqual(got, item.WantFLVXAfter) {
			t.Fatalf("%s: FLVX shared Core after = %v, want %v", item.Name, got, item.WantFLVXAfter)
		}
		if after.MachineProtection != nil && after.MachineProtection.Enabled && !reflect.DeepEqual(after.MachineProtection.closeWaitSeconds(), after.coreConfig().DefaultCloseWaitTimeoutSeconds) {
			t.Fatalf("%s: mmwx Core and FLVX shared Core disagree after the merge", item.Name)
		}
		if !reflect.DeepEqual(before.coreConfig().Limits, after.coreConfig().Limits) {
			t.Fatalf("%s: per-user CLOSE_WAIT changed: %#v -> %#v", item.Name, before.coreConfig().Limits, after.coreConfig().Limits)
		}
	}
	if checked < 5 {
		t.Fatalf("only %d v2 cases in the fixture", checked)
	}
}
