package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultConfigPath = "/etc/mmwxc-helper.env"
	defaultInterval   = 5 * time.Second
	defaultEndpoint   = "/api/custom/agent/metrics"
	detailedEndpoint  = "/api/custom/agent/connections"
	defaultCoreSocket = "/run/mmwxc/core-control.sock"
	defaultStatePath  = "/var/lib/mmwxc-helper/state.json"
	helperVersion     = "v0.3.6"
)

type config struct {
	CustomAPIURL string
	ServerID     string
	Token        string
	Interval     time.Duration
	CoreSocket   string
	StatePath    string
	EnableNft    bool
}

type connectionSnapshot struct {
	TCPCount        int64     `json:"tcp_count"`
	UDPCount        int64     `json:"udp_count"`
	ConnectionCount int64     `json:"connection_count"`
	SampledAt       time.Time `json:"sampled_at"`
}

type metricsPayload struct {
	ServerID        string `json:"server_id"`
	TCPCount        int64  `json:"tcp_count"`
	UDPCount        int64  `json:"udp_count"`
	ConnectionCount int64  `json:"connection_count"`
	SampledAt       string `json:"sampled_at"`
	HelperVersion   string `json:"helper_version"`
}

func main() {
	configPath := flag.String("config", defaultConfigPath, "path to env config file")
	once := flag.Bool("once", false, "collect once, upload once, then exit")
	printOnly := flag.Bool("print", false, "collect once and print JSON without uploading")
	showVersion := flag.Bool("version", false, "print version and exit")
	managedHelperUpdate := flag.Bool("managed-helper-update", false, "run the internal managed update worker")
	updateURL := flag.String("update-url", "", "managed update artifact URL")
	updateSHA256 := flag.String("update-sha256", "", "managed update artifact SHA256")
	updateVersion := flag.String("update-version", "", "managed update artifact version")
	flag.Parse()

	if *showVersion {
		fmt.Printf("mmwxc-helper %s\n", helperVersion)
		return
	}
	if *managedHelperUpdate {
		defer cleanupManagedUpdateWorker()
		time.Sleep(2 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		artifact := managementArtifact{URL: *updateURL, SHA256: *updateSHA256, Version: *updateVersion}
		if err := runManagedHelperUpdate(ctx, artifact, *configPath); err != nil {
			log.Fatalf("[mmwxc-helper] managed update failed: %v", err)
		}
		return
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("[mmwxc-helper] config error: %v", err)
	}

	client := &http.Client{Timeout: 8 * time.Second}
	core := newCoreClient(cfg.CoreSocket)
	state, err := loadLocalState(cfg.StatePath)
	if err != nil {
		log.Fatalf("[mmwxc-helper] state error: %v", err)
	}
	onlineTracker := newOnlineIPTracker()
	nftables := newNftablesManager(cfg.EnableNft)
	lifecycle := newLifecycleManager(core)
	executor := &commandExecutor{lifecycle: lifecycle, core: core, state: &state}
	if *printOnly {
		snapshot := collectDetailedSnapshot(context.Background(), core, onlineTracker, state.Settings)
		_ = json.NewEncoder(os.Stdout).Encode(snapshot)
		return
	}

	runOnce := func() {
		applyCtx, cancelApply := context.WithTimeout(context.Background(), 5*time.Second)
		if err := core.apply(applyCtx, state.Settings); err != nil {
			log.Printf("[mmwxc-helper] core config unavailable: %v", err)
		}
		cancelApply()
		snapshot := collectDetailedSnapshot(context.Background(), core, onlineTracker, state.Settings)
		if snapshot.Core.Available {
			applyNftables(nftables, snapshot, state.Settings)
		}
		state.HeartbeatAt = time.Now().UTC()
		state.HelperVersion = helperVersion
		if err := saveLocalState(cfg.StatePath, state); err != nil {
			log.Printf("[mmwxc-helper] save heartbeat failed: %v", err)
		}
		report := &managementReport{Status: executor.status(context.Background()), Result: state.PendingResult}
		response, err := uploadDetailedMetrics(context.Background(), client, cfg, snapshot, report)
		if err != nil {
			log.Printf("[mmwxc-helper] upload failed: %v", err)
			return
		}
		if state.PendingResult != nil {
			state.PendingResult = nil
		}
		state.ControllerConnectedAt = time.Now().UTC()
		state.Settings = response.Settings
		if response.Command != nil && !completedCommand(state.CompletedCommandIDs, response.Command.ID) {
			commandCtx, cancelCommand := context.WithTimeout(context.Background(), 5*time.Minute)
			result := executor.execute(commandCtx, *response.Command, cfg.Token)
			cancelCommand()
			state.PendingResult = &result
			state.LastOperation = &result
			rememberCompletedCommand(&state, response.Command.ID)
		}
		if err := saveLocalState(cfg.StatePath, state); err != nil {
			log.Printf("[mmwxc-helper] save state failed: %v", err)
		}
		applyCtx, cancelApply = context.WithTimeout(context.Background(), 5*time.Second)
		if err := core.apply(applyCtx, state.Settings); err != nil {
			log.Printf("[mmwxc-helper] apply updated core config failed: %v", err)
		}
		cancelApply()
		if snapshot.Core.Available {
			applyNftables(nftables, snapshot, state.Settings)
		}
	}

	runOnce()
	if *once {
		return
	}

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for range ticker.C {
		runOnce()
	}
}

func applyNftables(manager *nftablesManager, snapshot detailedConnectionSnapshot, settings connectionSettings) {
	policies, warnings := deriveInboundIPPolicies(coreSnapshotFromDetailed(snapshot), settings)
	for _, warning := range warnings {
		log.Printf("[mmwxc-helper] IP limiter: %s", warning)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.apply(ctx, policies, time.Duration(settings.OnlineIPGracePeriodSeconds)*time.Second); err != nil {
		log.Printf("[mmwxc-helper] IP limiter apply failed: %v", err)
	}
}

func loadConfig(path string) (config, error) {
	values := map[string]string{}
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return config{}, err
	}

	get := func(keys ...string) string {
		for _, key := range keys {
			if value := strings.TrimSpace(os.Getenv(key)); value != "" {
				return value
			}
		}
		for _, key := range keys {
			if value := strings.TrimSpace(values[key]); value != "" {
				return value
			}
		}
		return ""
	}
	getLegacy := func(primary, legacy string) string {
		if value := get(primary); value != "" {
			return value
		}
		return get(legacy)
	}

	interval := defaultInterval
	if raw := getLegacy("MMWXC_HELPER_INTERVAL", "INTERVAL"); raw != "" {
		parsed, err := parseInterval(raw)
		if err != nil {
			return config{}, err
		}
		interval = parsed
	}

	cfg := config{
		CustomAPIURL: strings.TrimRight(getLegacy("MMWXC_HELPER_API_URL", "CUSTOM_API_URL"), "/"),
		ServerID:     getLegacy("MMWXC_HELPER_SERVER_ID", "SERVER_ID"),
		Token:        getLegacy("MMWXC_HELPER_TOKEN", "TOKEN"),
		Interval:     interval,
		CoreSocket:   get("MMWXC_HELPER_CORE_SOCKET"),
		StatePath:    get("MMWXC_HELPER_STATE_FILE"),
	}
	if cfg.CoreSocket == "" {
		cfg.CoreSocket = defaultCoreSocket
	}
	if cfg.StatePath == "" {
		cfg.StatePath = defaultStatePath
	}
	if raw := get("MMWXC_HELPER_ENABLE_NFTABLES"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return config{}, fmt.Errorf("invalid MMWXC_HELPER_ENABLE_NFTABLES %q", raw)
		}
		cfg.EnableNft = enabled
	}
	if cfg.CustomAPIURL == "" {
		return config{}, errors.New("CUSTOM_API_URL is required")
	}
	if cfg.ServerID == "" {
		return config{}, errors.New("SERVER_ID is required")
	}
	if cfg.Token == "" {
		return config{}, errors.New("TOKEN is required")
	}
	if cfg.Interval < time.Second {
		return config{}, errors.New("INTERVAL must be at least 1s")
	}
	return cfg, nil
}

func parseInterval(raw string) (time.Duration, error) {
	if d, err := time.ParseDuration(raw); err == nil {
		return d, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid INTERVAL %q", raw)
	}
	return time.Duration(seconds) * time.Second, nil
}

func collectDetailedSnapshot(ctx context.Context, core *coreClient, tracker *onlineIPTracker, settings connectionSettings) detailedConnectionSnapshot {
	snapshot := detailedConnectionSnapshot{SampledAt: time.Now().UTC()}
	sockets, socketErr := readTCPSockets()
	if socketErr == nil {
		snapshot.System = summarizeTCP(sockets)
	}
	coreSnapshot, coreErr := core.snapshot(ctx)
	if coreErr != nil {
		snapshot.Core = coreStatus{Available: false, Error: coreErr.Error()}
		return snapshot
	}
	snapshot.Core = coreStatus{Available: true, Version: coreSnapshot.Version, StartedAt: coreSnapshot.StartedAt}
	snapshot.Inbounds, snapshot.ProxyUsers = tracker.aggregate(sockets, coreSnapshot, settings)
	return snapshot
}

func coreSnapshotFromDetailed(snapshot detailedConnectionSnapshot) coreSnapshotResponse {
	core := coreSnapshotResponse{Version: snapshot.Core.Version, StartedAt: snapshot.Core.StartedAt}
	for _, user := range snapshot.ProxyUsers {
		core.Users = append(core.Users, coreUserSnapshot{
			Identity:      user.Identity,
			InboundPort:   user.InboundPort,
			Attributed:    true,
			InboundActive: user.InboundActive,
		})
	}
	return core
}

func uploadMetrics(ctx context.Context, client *http.Client, cfg config, snapshot connectionSnapshot) error {
	payload := metricsPayload{
		ServerID:        cfg.ServerID,
		TCPCount:        snapshot.TCPCount,
		UDPCount:        snapshot.UDPCount,
		ConnectionCount: snapshot.ConnectionCount,
		SampledAt:       snapshot.SampledAt.Format(time.RFC3339),
		HelperVersion:   helperVersion,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.CustomAPIURL+defaultEndpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

func uploadDetailedMetrics(ctx context.Context, client *http.Client, cfg config, snapshot detailedConnectionSnapshot, management *managementReport) (detailedMetricsResponse, error) {
	legacy, err := readConnections()
	if err != nil {
		legacy = connectionSnapshot{TCPCount: snapshot.System.Total, ConnectionCount: snapshot.System.Total, SampledAt: snapshot.SampledAt}
	}
	payload := detailedMetricsPayload{
		ServerID: cfg.ServerID, HelperVersion: helperVersion, TCPCount: legacy.TCPCount, UDPCount: legacy.UDPCount,
		ConnectionCount: legacy.ConnectionCount, Snapshot: snapshot, Management: management,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return detailedMetricsResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.CustomAPIURL+detailedEndpoint, bytes.NewReader(body))
	if err != nil {
		return detailedMetricsResponse{}, err
	}
	request.Header.Set("Authorization", "Bearer "+cfg.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return detailedMetricsResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return detailedMetricsResponse{}, fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	var decoded detailedMetricsResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&decoded); err != nil {
		return detailedMetricsResponse{}, fmt.Errorf("decode controller response: %w", err)
	}
	if !decoded.Success {
		return detailedMetricsResponse{}, errors.New("controller rejected detailed metrics")
	}
	if decoded.Settings.OnlineIPGracePeriodSeconds <= 0 {
		decoded.Settings.OnlineIPGracePeriodSeconds = 30
	}
	if decoded.Settings.Users == nil {
		decoded.Settings.Users = []userConnectionSettings{}
	}
	return decoded, nil
}
