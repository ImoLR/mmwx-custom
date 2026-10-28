package flvx

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxNodes = 1000

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var uuid = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

type ValidationError struct {
	NodeID  string `json:"node_id"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

func ValidText(s string, max int) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) != "" && len(s) <= max && strings.IndexFunc(s, unicode.IsControl) < 0
}

func ValidateConfig(c Config) error {
	if c.PortMin < 1024 || c.PortMax > 65535 || c.PortMin > c.PortMax {
		return fmt.Errorf("端口段必须在 1024–65535 内")
	}
	if c.TrafficLimitBytes < 0 || c.TrafficLimitBytes > 9007199254740991 {
		return fmt.Errorf("流量上限无效")
	}
	if c.ResetDay < 1 || c.ResetDay > 31 {
		return fmt.Errorf("重置日必须在 1–31 内")
	}
	if c.MaxConnections < 0 || c.MaxConnections > 10000000 {
		return fmt.Errorf("连接上限无效")
	}
	if c.ShareUntil != nil && (*c.ShareUntil <= 0 || *c.ShareUntil > 253402300799999) {
		return fmt.Errorf("共享到期时间无效")
	}
	if c.MemoryMB < 64 || c.MemoryMB > 4096 || c.CPUPercent < 1 || c.CPUPercent > 400 {
		return fmt.Errorf("内存须为 64–4096 MB，CPU 须为 1–400%%")
	}
	return nil
}

// ValidateDesired checks a v1-only desired state: no extra protocol is
// advertised and busy ports are unknown.
func ValidateDesired(d Desired, c Config, official []int) []ValidationError {
	return ValidateDesiredWith(d, c, official, DesiredOptions{})
}

func ValidateDesiredWith(d Desired, c Config, official []int, o DesiredOptions) []ValidationError {
	errors := []ValidationError{}
	add := func(id, field, message string) { errors = append(errors, ValidationError{id, field, message}) }
	if d.Revision <= 0 || d.Revision > 9007199254740991 {
		add("", "revision", "revision must be a positive safe integer")
	}
	if d.Nodes == nil || len(d.Nodes) > MaxNodes {
		add("", "nodes", "nodes must be an array of at most 1000 nodes")
	}
	ids, ports, reserved := map[string]bool{}, map[int]bool{}, map[int]bool{}
	for _, p := range official {
		reserved[p] = true
	}
	busyTCP, busyUDP := map[int]bool{}, map[int]bool{}
	if o.Busy != nil {
		for _, p := range o.Busy.TCP {
			busyTCP[p] = true
		}
		for _, p := range o.Busy.UDP {
			busyUDP[p] = true
		}
	}
	for _, n := range d.Nodes {
		if !identifier.MatchString(n.ID) || ids[n.ID] {
			add(n.ID, "id", "invalid or duplicate node id")
		}
		ids[n.ID] = true
		if !identifier.MatchString(string(n.Owner.ID)) || !ValidText(n.Owner.Name, 128) {
			add(n.ID, "owner", "owner id and name are required (64 / 128 bytes maximum)")
		}
		if !ValidText(n.Name, 128) {
			add(n.ID, "name", "name is required (128 bytes maximum)")
		}
		if n.Port < c.PortMin || n.Port > c.PortMax || reserved[n.Port] || ports[n.Port] {
			add(n.ID, "port", "port is outside share range or collides with an official inbound / FLVX node")
		} else if busyTCP[n.Port] || UDPProtocol(n.Protocol) && busyUDP[n.Port] {
			add(n.ID, "port", "port is already in use on the server")
		}
		ports[n.Port] = true
		if n.Enabled == nil {
			add(n.ID, "enabled", "enabled is required")
		}
		if n.MaxConnections < 0 || n.MaxConnections > 10000000 {
			add(n.ID, "max_connections", "invalid connection limit")
		}
		if n.MaxOnlineIPs < 0 || n.MaxOnlineIPs > 1000000 {
			add(n.ID, "max_online_ips", "invalid online IP limit")
		}
		switch n.ConnMode {
		case "", ConnModeTotal, ConnModeInbound:
		default:
			add(n.ID, "conn_mode", "invalid connection mode")
		}
		if n.ExpiresAt != nil && (*n.ExpiresAt <= 0 || *n.ExpiresAt > 253402300799999) {
			add(n.ID, "expires_at", "invalid expiry")
		}
		switch n.Protocol {
		case "ss2022":
			if n.SS2022 == nil || n.objects() != 1 {
				add(n.ID, "ss2022", "SS2022 fields are required exclusively")
				continue
			}
			if n.SS2022.Method != "2022-blake3-aes-128-gcm" {
				add(n.ID, "ss2022.method", "unsupported SS2022 method")
			}
			for field, key := range map[string]string{"server_key": n.SS2022.ServerKey, "user_key": n.SS2022.UserKey} {
				b, err := base64.StdEncoding.Strict().DecodeString(key)
				if err != nil || len(b) != 16 || base64.StdEncoding.EncodeToString(b) != key {
					add(n.ID, "ss2022."+field, "key must be canonical base64 of 16 bytes")
				}
			}
		case "vless_reality_vision":
			if n.VLESS == nil || n.objects() != 1 {
				add(n.ID, "vless", "VLESS fields are required exclusively")
				continue
			}
			v := n.VLESS
			if !uuid.MatchString(v.UUID) {
				add(n.ID, "vless.uuid", "invalid UUID")
			}
			if v.Flow != "xtls-rprx-vision" {
				add(n.ID, "vless.flow", "flow must be xtls-rprx-vision")
			}
			key, err := base64.RawURLEncoding.Strict().DecodeString(v.Reality.PrivateKey)
			if err != nil || len(key) != 32 || base64.RawURLEncoding.EncodeToString(key) != v.Reality.PrivateKey {
				add(n.ID, "vless.reality.private_key", "invalid x25519 private key")
			} else if _, err := ecdh.X25519().NewPrivateKey(key); err != nil {
				add(n.ID, "vless.reality.private_key", "invalid x25519 private key")
			}
			host, port, err := net.SplitHostPort(v.Reality.Dest)
			p, _ := strconv.Atoi(port)
			if err != nil || !ValidHost(host) || p < 1 || p > 65535 || PrivateHost(host) {
				add(n.ID, "vless.reality.dest", "dest must be a public host:port")
			}
			if len(v.Reality.ServerNames) == 0 || len(v.Reality.ServerNames) > 16 {
				add(n.ID, "vless.reality.server_names", "1–16 server names required")
			}
			seen := map[string]bool{}
			for _, name := range v.Reality.ServerNames {
				if !ValidHost(name) || net.ParseIP(name) != nil || seen[name] {
					add(n.ID, "vless.reality.server_names", "invalid or duplicate server name")
				}
				seen[name] = true
			}
			seen = map[string]bool{}
			if len(v.Reality.ShortIDs) == 0 || len(v.Reality.ShortIDs) > 16 {
				add(n.ID, "vless.reality.short_ids", "1–16 short ids required")
			}
			for _, id := range v.Reality.ShortIDs {
				if _, err := hex.DecodeString(id); err != nil || len(id) > 16 || seen[id] {
					add(n.ID, "vless.reality.short_ids", "invalid or duplicate short id")
				}
				seen[id] = true
			}
		case ProtocolAnyTLS, ProtocolSnell, ProtocolMieru:
			capability, ok := o.Protocols[n.Protocol]
			if !ok {
				add(n.ID, "protocol", "protocol is not supported on this share")
				continue
			}
			validateExtra(n, capability, o.Now, func(field, message string) { add(n.ID, field, message) })
		default:
			add(n.ID, "protocol", "unsupported protocol")
		}
	}
	return errors
}

func ValidHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

var BlockCIDRs = []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8"}

func PrivateHost(host string) bool {
	a, err := netip.ParseAddr(host)
	if err != nil {
		return strings.EqualFold(strings.TrimSuffix(host, "."), "localhost")
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() {
		return true
	}
	for _, cidr := range BlockCIDRs {
		if netip.MustParsePrefix(cidr).Contains(a) {
			return true
		}
	}
	return false
}

// Official port lists can contain numbers, numeric strings or ranges.
func OfficialPorts(raw []byte) ([]int, error) {
	var config struct {
		Inbounds []struct {
			Port json.RawMessage `json:"port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	seen := map[int]bool{}
	result := []int{}
	for _, in := range config.Inbounds {
		s := strings.Trim(string(in.Port), `"`)
		if s == "" || s == "null" {
			continue
		}
		for _, part := range strings.Split(s, ",") {
			bounds := strings.Split(part, "-")
			start, err := strconv.Atoi(bounds[0])
			end := start
			if len(bounds) == 2 {
				end, err = strconv.Atoi(bounds[1])
			}
			if err != nil || len(bounds) > 2 || start < 1 || end > 65535 || end < start {
				return nil, fmt.Errorf("invalid official inbound port")
			}
			for p := start; p <= end; p++ {
				if !seen[p] {
					result = append(result, p)
					seen[p] = true
				}
			}
		}
	}
	return result, nil
}
