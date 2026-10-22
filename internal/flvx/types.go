// Package flvx contains the v1 structured contract shared by Custom and Helper.
package flvx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"
)

type OwnerID string

func (id *OwnerID) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*id = OwnerID(s)
		return nil
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil || n <= 0 {
		return fmt.Errorf("invalid owner id")
	}
	*id = OwnerID(strconv.FormatInt(n, 10))
	return nil
}

type Owner struct {
	ID   OwnerID `json:"id"`
	Name string  `json:"name"`
}

type SS2022 struct {
	Method    string `json:"method"`
	ServerKey string `json:"server_key"`
	UserKey   string `json:"user_key"`
}

type Reality struct {
	Dest        string   `json:"dest"`
	ServerNames []string `json:"server_names"`
	PrivateKey  string   `json:"private_key"`
	ShortIDs    []string `json:"short_ids"`
}

type VLESS struct {
	UUID    string  `json:"uuid"`
	Flow    string  `json:"flow"`
	Reality Reality `json:"reality"`
}

// Contract landing-protocols-v1: AnyTLS / Snell / Mieru nodes in the FLVX Core.
const (
	ProtocolAnyTLS   = "anytls"
	ProtocolSnell    = "snell"
	ProtocolMieru    = "mieru"
	ProtocolsVersion = 1
)

// ExtraProtocols lists the landing-protocols-v1 protocols in display order.
var ExtraProtocols = []string{ProtocolAnyTLS, ProtocolSnell, ProtocolMieru}

func IsExtraProtocol(p string) bool {
	return p == ProtocolAnyTLS || p == ProtocolSnell || p == ProtocolMieru
}

// AnyTLS runs over TLS with an FLVX-generated certificate. KeyPEM is a secret
// handled like Reality private keys: never logged, never echoed.
type AnyTLS struct {
	Password string    `json:"password"`
	Padding  string    `json:"padding"`
	TLS      AnyTLSTLS `json:"tls"`
}

type AnyTLSTLS struct {
	ServerName     string `json:"server_name"`
	CertificatePEM string `json:"certificate_pem"`
	KeyPEM         string `json:"key_pem"`
}

type Snell struct {
	PSK      string `json:"psk"`
	Version  int    `json:"version"`
	Obfs     string `json:"obfs"`
	ObfsHost string `json:"obfs_host"`
}

type Mieru struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ProtocolCap is one advertised extra protocol (contract §2). UDP = its UDP
// relay works on the share's Core; empty lists mean the contract defaults
// (Snell versions 4 and 5, obfs none/http/tls; Mieru TCP and UDP).
type ProtocolCap struct {
	UDP        bool     `json:"udp"`
	Versions   []int    `json:"versions,omitempty"`
	Obfs       []string `json:"obfs,omitempty"`
	Transports []string `json:"transports,omitempty"`
}

// ProtocolCaps is the share list's protocol_caps object.
type ProtocolCaps struct {
	Versions  []int                  `json:"versions"`
	Protocols map[string]ProtocolCap `json:"protocols"`
}

// ListeningPorts are the machine's listening ports inside the share range,
// excluding the FLVX Core's own inbounds (contract busy_ports).
type ListeningPorts struct {
	TCP []int `json:"tcp"`
	UDP []int `json:"udp"`
}

type Node struct {
	ID             string  `json:"id"`
	Owner          Owner   `json:"owner"`
	Name           string  `json:"name"`
	Protocol       string  `json:"protocol"`
	Port           int     `json:"port"`
	Enabled        *bool   `json:"enabled"`
	MaxConnections int64   `json:"max_connections"`
	MaxOnlineIPs   int     `json:"max_online_ips"`
	ExpiresAt      *int64  `json:"expires_at"`
	SS2022         *SS2022 `json:"ss2022,omitempty"`
	VLESS          *VLESS  `json:"vless,omitempty"`
	// landing-protocols-v1. Omitted when absent, so the signed payload of a
	// share without these nodes stays byte-identical for older Helpers.
	AnyTLS *AnyTLS `json:"anytls,omitempty"`
	Snell  *Snell  `json:"snell,omitempty"`
	Mieru  *Mieru  `json:"mieru,omitempty"`
}

func (n Node) Tag() string { return "flvxl-" + n.ID }

// Desired is FLVX's complete replacement state. Egress is omitted when absent
// so the signed Helper payload stays byte-identical for Helpers without
// landing-egress support.
type Desired struct {
	Revision int64   `json:"revision"`
	Nodes    []Node  `json:"nodes"`
	Egress   *Egress `json:"egress,omitempty"`
}

type Config struct {
	PortMin           int    `json:"port_min"`
	PortMax           int    `json:"port_max"`
	TrafficLimitBytes int64  `json:"traffic_limit_bytes"`
	ResetDay          int    `json:"reset_day"`
	MaxConnections    int64  `json:"max_connections"`
	ShareUntil        *int64 `json:"share_until"`
	MemoryMB          int    `json:"memory_mb"`
	CPUPercent        int    `json:"cpu_percent"`
}

type Hosts struct {
	IPv4   string `json:"ipv4"`
	IPv6   string `json:"ipv6"`
	Domain string `json:"domain"`
}

type LandingHosts struct {
	Native []string `json:"native_landing_hosts"`
	Nodes  []string `json:"flvx_node_hosts"`
}

type Core struct {
	State   string `json:"state"`
	Version string `json:"version,omitempty"`
	Epoch   string `json:"epoch,omitempty"`
	// Why the Helper cannot (re)start the shared Core, e.g. a parent directory
	// the FLVX Core account cannot enter. Shown to the operator only.
	Error string `json:"error,omitempty"`
}

type OnlineIP struct {
	IP          string `json:"ip"`
	Connections int    `json:"connections"`
}

type NodeStatus struct {
	ID            string      `json:"id"`
	State         string      `json:"state"`
	Error         string      `json:"error,omitempty"`
	UpBytes       int64       `json:"up_bytes"`
	DownBytes     int64       `json:"down_bytes"`
	Connections   int64       `json:"connections"`
	OnlineIPCount *int        `json:"online_ip_count,omitempty"`
	OnlineIPs     *[]OnlineIP `json:"online_ips,omitempty"`
}

type Status struct {
	AppliedRevision  int64        `json:"applied_revision"`
	Core             Core         `json:"core"`
	Nodes            []NodeStatus `json:"nodes"`
	TrafficUsedBytes int64        `json:"share_traffic_used_bytes"`
	CollectedAt      int64        `json:"collected_at"`
	// Reported only by Helpers with landing-egress support.
	Egress *EgressStatus `json:"egress,omitempty"`
	// landing-protocols-v1, reported only by Helpers that render the extra
	// protocols, and only for what the running FLVX Core can account for.
	Protocols map[string]ProtocolCap `json:"flvx_protocols,omitempty"`
	Listening *ListeningPorts        `json:"listening_ports,omitempty"`
}

// Report sequence and generation belong only to the authenticated Helper channel.
type Report struct {
	Status
	Generation string `json:"generation"`
	Sequence   int64  `json:"sequence"`
	Period     string `json:"period"`
}

type Artifact struct {
	URL               string `json:"url"`
	SHA256            string `json:"sha256"`
	Version           string `json:"version"`
	GitHubAccelerator string `json:"github_accelerator,omitempty"`
}

type Payload struct {
	ServerID         string   `json:"server_id"`
	Version          int64    `json:"version"`
	Status           string   `json:"status"`
	Config           Config   `json:"config"`
	Desired          Desired  `json:"desired"`
	OfficialPorts    []int    `json:"official_ports"`
	Artifact         Artifact `json:"artifact"`
	Period           string   `json:"period"`
	TrafficUsedBytes int64    `json:"traffic_used_bytes"`
	AckGeneration    string   `json:"ack_generation"`
	AckSequence      int64    `json:"ack_sequence"`
}

type Envelope struct {
	Payload   Payload `json:"payload"`
	ExpiresAt int64   `json:"expires_at"`
	Signature string  `json:"signature"`
}

func (e Envelope) signature(token string) string {
	key := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, key[:])
	b, _ := json.Marshal(struct {
		Payload   Payload
		ExpiresAt int64
	}{e.Payload, e.ExpiresAt})
	mac.Write([]byte("mmwxc-flvx-v1\n"))
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil))
}

func (e *Envelope) Sign(token string) { e.Signature = e.signature(token) }
func (e Envelope) Verify(token, serverID string, now time.Time) bool {
	return e.Payload.ServerID == serverID && e.ExpiresAt > now.UnixMilli() && e.ExpiresAt <= now.Add(11*time.Minute).UnixMilli() && hmac.Equal([]byte(e.Signature), []byte(e.signature(token)))
}

// Counter retains both the raw cursor and lifetime totals; a new epoch or a
// decreasing raw counter starts at zero without decreasing the published value.
type Counter struct {
	Epoch   string `json:"epoch"`
	RawUp   int64  `json:"raw_up"`
	RawDown int64  `json:"raw_down"`
	Up      int64  `json:"up"`
	Down    int64  `json:"down"`
}

func Delta(previous, current int64, reset bool) int64 {
	if current < 0 {
		return 0
	}
	if reset || current < previous {
		return current
	}
	return current - previous
}

func (c *Counter) Merge(epoch string, up, down int64) int64 {
	u, d := Delta(c.RawUp, up, c.Epoch != epoch), Delta(c.RawDown, down, c.Epoch != epoch)
	c.Up = AddBytes(c.Up, u)
	c.Down = AddBytes(c.Down, d)
	c.Epoch, c.RawUp, c.RawDown = epoch, up, down
	return AddBytes(u, d)
}

// Saturate cumulative byte counts rather than wrapping and reopening a quota.
func AddBytes(a, b int64) int64 {
	if b > math.MaxInt64-a {
		return math.MaxInt64
	}
	return a + b
}

// Monthly quotas reset at midnight in Taiwan; days beyond month-end clamp.
func Period(now time.Time, day int) string {
	if day < 1 || day > 31 {
		day = 1
	}
	now = now.In(time.FixedZone("Asia/Taipei", 8*3600))
	y, m, _ := now.Date()
	start := func(y int, m time.Month) time.Time {
		d := day
		if last := time.Date(y, m+1, 0, 0, 0, 0, 0, now.Location()).Day(); d > last {
			d = last
		}
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	}
	t := start(y, m)
	if now.Before(t) {
		t = start(y, m-1)
	}
	return t.Format("2006-01-02")
}

func Active(status string, cfg Config, used int64, n Node, now time.Time) bool {
	return status == "active" && (cfg.ShareUntil == nil || *cfg.ShareUntil > now.UnixMilli()) && (cfg.TrafficLimitBytes == 0 || used < cfg.TrafficLimitBytes) && n.Enabled != nil && *n.Enabled && (n.ExpiresAt == nil || *n.ExpiresAt > now.UnixMilli())
}
