package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultListenAddr    = "127.0.0.1:12890"
	defaultOrigins       = "http://178.214.214.173:5173,https://dev.mmwx.imgamer.top"
	defaultMMWXAPITarget = "http://127.0.0.1:12891"
	defaultFrontendDir   = "frontend/dist"
	defaultGeoIPToken    = "cddae164b36656"
	geoCacheSuccessTTL   = 24 * time.Hour
	geoCacheFailureTTL   = 10 * time.Minute
	maxGeoResponseBytes  = 1 << 20
	helperStaleTimeout   = 15 * time.Second
	minHelperReportGap   = time.Second
)

type systemMetrics struct {
	CPUPct    float64 `json:"cpu_pct"`
	CPUCores  int     `json:"cpu_cores,omitempty"`
	MemUsed   uint64  `json:"mem_used"`
	MemTotal  uint64  `json:"mem_total"`
	SwapUsed  uint64  `json:"swap_used"`
	SwapTotal uint64  `json:"swap_total"`
	DiskUsed  uint64  `json:"disk_used"`
	DiskTotal uint64  `json:"disk_total"`
	HasCPU    bool    `json:"has_cpu"`
	HasMem    bool    `json:"has_mem"`
	HasDisk   bool    `json:"has_disk"`
	LoadAvg   string  `json:"loadavg,omitempty"`
	Hostname  string  `json:"hostname,omitempty"`
	OS        string  `json:"os,omitempty"`
}

type cpuTimes struct {
	user    uint64
	nice    uint64
	system  uint64
	idle    uint64
	iowait  uint64
	irq     uint64
	softirq uint64
	steal   uint64
}

type memoryStats struct {
	memTotal     uint64
	memAvailable uint64
	swapTotal    uint64
	swapFree     uint64
	hasMemTotal  bool
	hasMemAvail  bool
	hasSwapTotal bool
	hasSwapFree  bool
}

type diskStats struct {
	used  uint64
	total uint64
}

type geoLookupResponse struct {
	Success     bool   `json:"success"`
	CountryCode string `json:"country_code,omitempty"`
	Country     string `json:"country,omitempty"`
	Flag        string `json:"flag,omitempty"`
	Message     string `json:"message,omitempty"`
	Cached      bool   `json:"cached"`
}

type geoIPResponse struct {
	CountryCode string `json:"country_code"`
}

type geoCacheEntry struct {
	response  geoLookupResponse
	expiresAt time.Time
}

type geoInflightCall struct {
	done     chan struct{}
	response geoLookupResponse
}

type app struct {
	allowedOrigins map[string]struct{}
	apiToken       string
	geoIPToken     string
	helperTokens   map[string]string
	helperState    *helperState
	mmwxAPITarget  *url.URL
	publicURL      string

	mu      sync.Mutex
	lastCPU cpuTimes
	hasCPU  bool

	connectionMu      sync.Mutex
	connectionMetrics map[string]connectionMetrics
	helperRate        map[string]time.Time

	geoMu       sync.Mutex
	geoCache    map[string]geoCacheEntry
	geoInflight map[string]*geoInflightCall
	geoSlots    chan struct{}
}

func main() {
	listenAddr := getenv("MMWXC_API_LISTEN_ADDR", defaultListenAddr)
	mmwxAPITarget, err := url.Parse(getenv("MMWX_API_TARGET", defaultMMWXAPITarget))
	if err != nil {
		log.Fatalf("[mmwx-custom] invalid MMWX_API_TARGET: %v", err)
	}
	api := &app{
		allowedOrigins:    parseOrigins(getenv("MMWXC_ALLOWED_ORIGINS", defaultOrigins)),
		apiToken:          os.Getenv("MMWXC_API_TOKEN"),
		geoIPToken:        getenv("MMWXC_GEOIP_TOKEN", defaultGeoIPToken),
		helperTokens:      parseHelperTokens(os.Getenv("MMWXC_HELPER_TOKENS")),
		mmwxAPITarget:     mmwxAPITarget,
		publicURL:         strings.TrimRight(os.Getenv("MMWXC_PUBLIC_URL"), "/"),
		connectionMetrics: make(map[string]connectionMetrics),
		helperRate:        make(map[string]time.Time),
		geoCache:          make(map[string]geoCacheEntry),
		geoInflight:       make(map[string]*geoInflightCall),
		geoSlots:          make(chan struct{}, 4),
	}
	api.helperState, err = openHelperState(getenv("MMWXC_HELPER_STATE_FILE", defaultHelperStatePath), helperInstallTokenTTLFromEnv())
	if err != nil {
		log.Fatalf("[mmwx-custom] helper state failed: %v", err)
	}
	if first, err := readProcStatCPU(); err == nil {
		api.lastCPU = first
		api.hasCPU = true
	}
	if api.apiToken == "" {
		log.Printf("[mmwx-custom] warning: MMWXC_API_TOKEN is not set; system metrics endpoint is unauthenticated")
	}
	if len(api.helperTokens) == 0 {
		log.Printf("[mmwx-custom] warning: MMWXC_HELPER_TOKENS is not set; helper metrics POST endpoint is disabled")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", api.withCORS(api.healthz))
	mux.HandleFunc("/api/dashboard/system", api.withCORS(api.system))
	mux.HandleFunc("/api/custom/dashboard/system", api.withCORS(api.system))
	mux.HandleFunc("/api/geo/lookup", api.withCORS(api.geoLookup))
	mux.HandleFunc("/api/custom/geo/lookup", api.withCORS(api.geoLookup))
	mux.HandleFunc("/api/custom/agent/metrics", api.withCORS(api.connectionMetricsHandler))
	mux.HandleFunc("/api/custom/helper/install-token", api.withCORS(api.createHelperInstallTokenHandler))
	mux.HandleFunc("/api/custom/helper/install/", api.withCORS(api.helperInstallScriptHandler))
	mux.Handle("/api/", api.withCORSHandler(mmwxAPIProxy(mmwxAPITarget)))
	mux.Handle("/", spaHandler(getenv("MMWXC_FRONTEND_DIR", defaultFrontendDir)))

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("[mmwx-custom] listening on %s", listenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[mmwx-custom] server failed: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("[mmwx-custom] graceful shutdown failed: %v", err)
	}
}

func (a *app) withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if _, ok := a.allowedOrigins[origin]; ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, MM-Authorization, Content-Type")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

func (a *app) withCORSHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.withCORS(next.ServeHTTP)(w, r)
	})
}

func (a *app) healthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": "ok"})
}

func mmwxAPIProxy(target *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)
	baseDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		baseDirector(r)
		r.Host = target.Host
	}
	return proxy
}

func spaHandler(frontendDir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
			return
		}
		cleanPath := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if cleanPath == "." {
			cleanPath = "index.html"
		}
		if cleanPath == ".." || strings.HasPrefix(cleanPath, "../") {
			http.NotFound(w, r)
			return
		}
		filePath := filepath.Join(frontendDir, cleanPath)
		if info, err := os.Stat(filePath); err == nil && !info.IsDir() {
			http.ServeFile(w, r, filePath)
			return
		}
		indexPath := filepath.Join(frontendDir, "index.html")
		if _, err := os.Stat(indexPath); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "message": "custom UI frontend is not built"})
			return
		}
		http.ServeFile(w, r, indexPath)
	})
}

func (a *app) system(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	if !a.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "unauthorized"})
		return
	}
	metrics, err := a.snapshot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "failed to read system metrics"})
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (a *app) geoLookup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method not allowed"})
		return
	}
	host, ok := normalizeLookupHost(r.URL.Query().Get("host"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, geoLookupResponse{Success: false, Message: "invalid host"})
		return
	}

	if cached, ok := a.getGeoCache(host); ok {
		cached.Cached = true
		writeJSON(w, http.StatusOK, cached)
		return
	}

	writeJSON(w, http.StatusOK, a.lookupGeoDeduplicated(r.Context(), host))
}

// lookupGeoDeduplicated shares one DNS/IPInfo operation for each normalized host.
// The operation owns its timeout so one disconnected caller does not cancel peers.
func (a *app) lookupGeoDeduplicated(requestCtx context.Context, host string) geoLookupResponse {
	a.geoMu.Lock()
	if cached, ok := a.getGeoCacheLocked(host, time.Now()); ok {
		a.geoMu.Unlock()
		cached.Cached = true
		return cached
	}
	if call, ok := a.geoInflight[host]; ok {
		a.geoMu.Unlock()
		select {
		case <-call.done:
			return call.response
		case <-requestCtx.Done():
			return unknownGeoResponse("timeout", false)
		}
	}
	call := &geoInflightCall{done: make(chan struct{})}
	a.geoInflight[host] = call
	a.geoMu.Unlock()

	operationCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	select {
	case a.geoSlots <- struct{}{}:
		defer func() { <-a.geoSlots }()
	case <-operationCtx.Done():
		a.completeGeoInflight(host, call, unknownGeoResponse("timeout", false))
		return unknownGeoResponse("timeout", false)
	}

	response := a.lookupGeo(operationCtx, host)
	a.setGeoCache(host, response)
	a.completeGeoInflight(host, call, response)
	return response
}

func (a *app) completeGeoInflight(host string, call *geoInflightCall, response geoLookupResponse) {
	a.geoMu.Lock()
	call.response = response
	delete(a.geoInflight, host)
	close(call.done)
	a.geoMu.Unlock()
}

func (a *app) lookupGeo(ctx context.Context, host string) geoLookupResponse {
	ip, ok := resolvePublicIP(ctx, host)
	if !ok {
		return unknownGeoResponse("unknown", false)
	}
	if cached, ok := a.getGeoCache(ip.String()); ok {
		cached.Cached = true
		return cached
	}
	if a.geoIPToken == "" {
		return unknownGeoResponse("unknown", false)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipinfo.io/lite/"+url.PathEscape(ip.String())+"?token="+url.QueryEscape(a.geoIPToken), nil)
	if err != nil {
		return unknownGeoResponse("unknown", false)
	}
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return unknownGeoResponse("unknown", false)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return unknownGeoResponse("unknown", false)
	}
	var body geoIPResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxGeoResponseBytes)).Decode(&body); err != nil {
		return unknownGeoResponse("unknown", false)
	}
	code := strings.ToUpper(strings.TrimSpace(body.CountryCode))
	if len(code) != 2 {
		return unknownGeoResponse("unknown", false)
	}
	response := geoLookupResponse{
		Success:     true,
		CountryCode: code,
		Country:     countryLabel(code),
		Flag:        flagFromCountryCode(code),
	}
	a.setGeoCache(ip.String(), response)
	return response
}

func (a *app) getGeoCache(key string) (geoLookupResponse, bool) {
	a.geoMu.Lock()
	defer a.geoMu.Unlock()
	return a.getGeoCacheLocked(key, time.Now())
}

func (a *app) getGeoCacheLocked(key string, now time.Time) (geoLookupResponse, bool) {
	entry, ok := a.geoCache[key]
	if !ok || now.After(entry.expiresAt) {
		if ok {
			delete(a.geoCache, key)
		}
		return geoLookupResponse{}, false
	}
	return entry.response, true
}

func (a *app) setGeoCache(key string, response geoLookupResponse) {
	ttl := geoCacheSuccessTTL
	if response.CountryCode == "" {
		ttl = geoCacheFailureTTL
	}
	a.geoMu.Lock()
	a.geoCache[key] = geoCacheEntry{response: response, expiresAt: time.Now().Add(ttl)}
	a.geoMu.Unlock()
}

func normalizeLookupHost(value string) (string, bool) {
	host := strings.TrimSpace(value)
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if host == "" || len(host) > 253 || strings.ContainsAny(host, "/?#\\") {
		return "", false
	}
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = strings.TrimPrefix(strings.TrimSuffix(splitHost, "]"), "[")
	}
	if host == "" || strings.ContainsAny(host, "\x00\r\n\t ") {
		return "", false
	}
	return host, true
}

func resolvePublicIP(ctx context.Context, host string) (net.IP, bool) {
	if ip := net.ParseIP(host); ip != nil {
		if isPublicIP(ip) {
			return ip, true
		}
		return nil, false
	}
	resolveCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(resolveCtx, host)
	if err != nil || len(addrs) == 0 {
		return nil, false
	}
	var firstIPv6 net.IP
	for _, addr := range addrs {
		ip := addr.IP
		if !isPublicIP(ip) {
			continue
		}
		if ip.To4() != nil {
			return ip, true
		}
		if firstIPv6 == nil {
			firstIPv6 = ip
		}
	}
	if firstIPv6 != nil {
		return firstIPv6, true
	}
	return nil, false
}

func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	return true
}

func unknownGeoResponse(message string, cached bool) geoLookupResponse {
	return geoLookupResponse{Success: true, Message: message, Cached: cached}
}

func flagFromCountryCode(code string) string {
	if len(code) != 2 {
		return ""
	}
	runes := []rune(strings.ToUpper(code))
	return string([]rune{0x1f1a5 + runes[0], 0x1f1a5 + runes[1]})
}

func countryLabel(code string) string {
	labels := map[string]string{
		"CN": "中国", "HK": "香港", "MO": "澳门", "TW": "台湾", "JP": "日本", "KR": "韩国",
		"SG": "新加坡", "TH": "泰国", "VN": "越南", "PH": "菲律宾", "MY": "马来西亚", "ID": "印尼",
		"US": "美国", "CA": "加拿大", "MX": "墨西哥", "BR": "巴西", "GB": "英国", "DE": "德国",
		"FR": "法国", "NL": "荷兰", "IT": "意大利", "ES": "西班牙", "RU": "俄罗斯", "AU": "澳大利亚",
	}
	if label, ok := labels[strings.ToUpper(code)]; ok {
		return label
	}
	return strings.ToUpper(code)
}

func (a *app) authorized(r *http.Request) bool {
	if a.apiToken == "" {
		return true
	}
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	return strings.HasPrefix(value, prefix) && strings.TrimSpace(strings.TrimPrefix(value, prefix)) == a.apiToken
}

func (a *app) snapshot() (systemMetrics, error) {
	mem, err := readProcMeminfo()
	if err != nil {
		return systemMetrics{}, err
	}
	disk, err := localDiskUsage("/")
	if err != nil {
		return systemMetrics{}, err
	}
	memUsed := uint64(0)
	if mem.hasMemTotal && mem.hasMemAvail && mem.memTotal >= mem.memAvailable {
		memUsed = mem.memTotal - mem.memAvailable
	}
	swapUsed := uint64(0)
	if mem.hasSwapTotal && mem.hasSwapFree && mem.swapTotal >= mem.swapFree {
		swapUsed = mem.swapTotal - mem.swapFree
	}
	hostname, _ := os.Hostname()
	return systemMetrics{
		CPUPct:    a.cpuPercent(),
		CPUCores:  runtime.NumCPU(),
		MemUsed:   memUsed,
		MemTotal:  mem.memTotal,
		SwapUsed:  swapUsed,
		SwapTotal: mem.swapTotal,
		DiskUsed:  disk.used,
		DiskTotal: disk.total,
		HasCPU:    true,
		HasMem:    mem.hasMemTotal,
		HasDisk:   disk.total > 0,
		LoadAvg:   readLoadAvg(),
		Hostname:  hostname,
		OS:        runtime.GOOS,
	}, nil
}

func (a *app) cpuPercent() float64 {
	cur, err := readProcStatCPU()
	if err != nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.hasCPU {
		a.lastCPU = cur
		a.hasCPU = true
		return 0
	}
	prev := a.lastCPU
	a.lastCPU = cur
	idleDelta := diffCPU(cur.idle+cur.iowait, prev.idle+prev.iowait)
	totalDelta := diffCPU(cur.total(), prev.total())
	if totalDelta == 0 || idleDelta > totalDelta {
		return 0
	}
	return clampPercent(float64(totalDelta-idleDelta) / float64(totalDelta) * 100)
}

func readProcStatCPU() (cpuTimes, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 || fields[0] != "cpu" {
			continue
		}
		values := make([]uint64, 8)
		for i := range values {
			n, err := strconv.ParseUint(fields[i+1], 10, 64)
			if err != nil {
				return cpuTimes{}, err
			}
			values[i] = n
		}
		return cpuTimes{
			user: values[0], nice: values[1], system: values[2], idle: values[3],
			iowait: values[4], irq: values[5], softirq: values[6], steal: values[7],
		}, nil
	}
	return cpuTimes{}, errors.New("cpu line not found")
}

func readProcMeminfo() (memoryStats, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return memoryStats{}, err
	}
	stats := memoryStats{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		valueKiB, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		valueBytes := valueKiB * 1024
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			stats.memTotal = valueBytes
			stats.hasMemTotal = true
		case "MemAvailable":
			stats.memAvailable = valueBytes
			stats.hasMemAvail = true
		case "MemFree":
			if !stats.hasMemAvail {
				stats.memAvailable = valueBytes
			}
		case "SwapTotal":
			stats.swapTotal = valueBytes
			stats.hasSwapTotal = true
		case "SwapFree":
			stats.swapFree = valueBytes
			stats.hasSwapFree = true
		}
	}
	return stats, nil
}

func localDiskUsage(path string) (diskStats, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return diskStats{}, err
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	used := uint64(0)
	if total >= free {
		used = total - free
	}
	return diskStats{used: used, total: total}, nil
}

func readLoadAvg() string {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return ""
	}
	return strings.Join(fields[:3], " ")
}

func (t cpuTimes) total() uint64 {
	return t.user + t.nice + t.system + t.idle + t.iowait + t.irq + t.softirq + t.steal
}

func diffCPU(cur, prev uint64) uint64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}

func clampPercent(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func parseOrigins(value string) map[string]struct{} {
	origins := make(map[string]struct{})
	for _, item := range strings.Split(value, ",") {
		origin := strings.TrimSpace(item)
		if origin != "" {
			origins[origin] = struct{}{}
		}
	}
	return origins
}

func getenv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
