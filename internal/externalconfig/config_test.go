package externalconfig

import (
	"encoding/json"
	"testing"
)

const completeConfig = `{"log":{"loglevel":"error"},"api":{"tag":"api","services":["HandlerService","LoggerService","StatsService","RoutingService"]},"metrics":{"tag":"Metrics","listen":"127.0.0.1:38889"},"inbounds":[{"tag":"user","port":"10000-10005"},{"tag":"api","listen":"127.0.0.1","port":46736,"protocol":"dokodemo-door"}],"routing":{"rules":[{"type":"field","inboundTag":["api"],"outboundTag":"api"}]}}`

func TestExternalControlShapeAndProtectedUserConfig(t *testing.T) {
	if ok, err := Complete([]byte(completeConfig)); err != nil || !ok {
		t.Fatalf("official config: %t %v", ok, err)
	}
	before, _ := UserConfigHash([]byte(completeConfig))
	for _, missing := range []string{"api", "metrics", "api inbound", "api route"} {
		t.Run(missing, func(t *testing.T) {
			var config map[string]any
			_ = json.Unmarshal([]byte(completeConfig), &config)
			switch missing {
			case "api inbound":
				config["inbounds"] = config["inbounds"].([]any)[:1]
			case "api route":
				delete(config, "routing")
			default:
				delete(config, missing)
			}
			data, _ := json.Marshal(config)
			if ok, err := Complete(data); ok || err != nil {
				t.Fatalf("missing %s: %t %v", missing, ok, err)
			}
			if after, _ := UserConfigHash(data); before != after {
				t.Fatalf("control-only change affected user fingerprint")
			}
		})
	}
	var config map[string]any
	_ = json.Unmarshal([]byte(completeConfig), &config)
	config["log"] = map[string]any{"access": "/tmp/custom-access"}
	data, _ := json.Marshal(config)
	if after, _ := UserConfigHash(data); before == after {
		t.Fatal("unrelated log change was ignored")
	}
	config["inbounds"].([]any)[1].(map[string]any)["protocol"] = "snell"
	data, _ = json.Marshal(config)
	if _, err := Complete(data); err == nil {
		t.Fatal("conflicting api tag accepted")
	}
}

func TestExternalSystemSettingsKeepExplicitPorts(t *testing.T) {
	current := map[string]any{"metrics_listen": "127.0.0.1:12345", "grpc_port": float64(12346), "future": true, "metrics_enabled": false}
	got := Settings(current)
	if got["metrics_listen"] != current["metrics_listen"] || got["grpc_port"] != current["grpc_port"] || got["future"] != true || got["stats_enabled"] != true || got["grpc_enabled"] != true || got["metrics_enabled"] != true {
		t.Fatalf("settings=%v", got)
	}
	if current["metrics_enabled"] != false {
		t.Fatal("input mutated")
	}
	defaults := Settings(nil)
	if defaults["grpc_port"] != 46736 || defaults["metrics_listen"] != "127.0.0.1:38889" {
		t.Fatalf("defaults=%v", defaults)
	}
}
