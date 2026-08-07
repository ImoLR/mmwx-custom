package main

import (
	"bufio"
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
	helperVersion     = "v0.1.0"
)

type config struct {
	CustomAPIURL string
	ServerID     string
	Token        string
	Interval     time.Duration
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
	flag.Parse()

	if *showVersion {
		fmt.Printf("mmwxc-helper %s\n", helperVersion)
		return
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("[mmwxc-helper] config error: %v", err)
	}

	client := &http.Client{Timeout: 8 * time.Second}
	if *printOnly {
		snapshot, err := readConnections()
		if err != nil {
			log.Fatalf("[mmwxc-helper] read connections: %v", err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(snapshot)
		return
	}

	runOnce := func() {
		snapshot, err := readConnections()
		if err != nil {
			log.Printf("[mmwxc-helper] read connections failed: %v", err)
			return
		}
		if err := uploadMetrics(context.Background(), client, cfg, snapshot); err != nil {
			log.Printf("[mmwxc-helper] upload failed: %v", err)
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

func readConnections() (connectionSnapshot, error) {
	tcp, okTCP := countProcNetRows("/proc/net/tcp")
	tcp6, okTCP6 := countProcNetRows("/proc/net/tcp6")
	udp, okUDP := countProcNetRows("/proc/net/udp")
	udp6, okUDP6 := countProcNetRows("/proc/net/udp6")
	if !okTCP && !okTCP6 && !okUDP && !okUDP6 {
		return connectionSnapshot{}, errors.New("no /proc/net socket tables are readable")
	}
	tcpCount := tcp + tcp6
	udpCount := udp + udp6
	return connectionSnapshot{
		TCPCount:        tcpCount,
		UDPCount:        udpCount,
		ConnectionCount: tcpCount + udpCount,
		SampledAt:       time.Now().UTC(),
	}, nil
}

func countProcNetRows(path string) (int64, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer file.Close()

	var lines int64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines++
	}
	if scanner.Err() != nil {
		return 0, false
	}
	if lines == 0 {
		return 0, true
	}
	return lines - 1, true
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
