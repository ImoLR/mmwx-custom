package flvx

import (
	"strconv"
	"strings"
)

// BaseConfig is read only when the Core starts. RoutingService enables hot
// rule replacement for landing egress plans; IPOnDemand resolves a domain at
// the first IP condition (the safety rule), so no later domain rule can route
// a name that resolves to a private address. Policy timeouts are the Core
// defaults, stated explicitly so half-open connections are reaped.
func BaseConfig(apiPort int) map[string]any {
	return map[string]any{
		"log":   map[string]any{"loglevel": "warning", "access": "none"},
		"api":   map[string]any{"tag": "flvx-api", "listen": "127.0.0.1:" + strconv.Itoa(apiPort), "services": []string{"HandlerService", "StatsService", "RoutingService"}},
		"stats": map[string]any{},
		"policy": map[string]any{
			"levels": map[string]any{"0": map[string]any{"handshake": 4, "connIdle": 300, "uplinkOnly": 2, "downlinkOnly": 5}},
			"system": map[string]any{"statsInboundUplink": true, "statsInboundDownlink": true},
		},
		"inbounds":  []any{},
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom", "settings": map[string]any{"domainStrategy": "UseIP"}}, map[string]any{"tag": "block", "protocol": "blackhole"}},
		"routing":   map[string]any{"domainStrategy": "IPOnDemand", "rules": []any{SafetyRule()}},
	}
}

func Inbound(n Node) map[string]any {
	result := map[string]any{"tag": n.Tag(), "listen": "0.0.0.0", "port": n.Port,
		// Preserve the original target for routing: an attacker-controlled Host
		// or TLS SNI must never hide an original private destination.
		"sniffing": map[string]any{"enabled": true, "destOverride": []string{"http", "tls"}, "routeOnly": true, "domainsExcluded": []string{"regexp:.*"}},
	}
	if n.Protocol == "ss2022" && n.SS2022 != nil {
		result["protocol"] = "shadowsocks"
		result["settings"] = map[string]any{"method": n.SS2022.Method, "password": n.SS2022.ServerKey, "network": "tcp,udp", "clients": []any{map[string]any{"password": n.SS2022.UserKey, "email": n.Tag()}}}
	} else if n.VLESS != nil {
		v := n.VLESS
		result["protocol"] = "vless"
		result["settings"] = map[string]any{"decryption": "none", "clients": []any{map[string]any{"id": v.UUID, "email": n.Tag(), "flow": "xtls-rprx-vision"}}}
		result["streamSettings"] = map[string]any{"network": "tcp", "security": "reality", "realitySettings": map[string]any{"dest": v.Reality.Dest, "serverNames": v.Reality.ServerNames, "privateKey": v.Reality.PrivateKey, "shortIds": v.Reality.ShortIDs}}
	} else if n.Protocol == ProtocolAnyTLS && n.AnyTLS != nil {
		// landing-protocols-v1 §6. Padding "default" is the Core's built-in
		// scheme (no paddingScheme); the certificate and key go inline, so no
		// secret is written to disk. server_name is not rendered.
		a := n.AnyTLS
		result["protocol"] = "anytls"
		result["settings"] = map[string]any{"users": []any{map[string]any{"password": a.Password, "email": n.Tag()}}}
		result["streamSettings"] = map[string]any{"network": "tcp", "security": "tls", "tlsSettings": map[string]any{"minVersion": "1.2", "alpn": []string{"h2", "http/1.1"},
			"certificates": []any{map[string]any{"certificate": pemLines(a.TLS.CertificatePEM), "key": pemLines(a.TLS.KeyPEM)}}}}
	} else if n.Protocol == ProtocolSnell && n.Snell != nil {
		user := map[string]any{"psk": n.Snell.PSK, "version": n.Snell.Version, "email": n.Tag()}
		if n.Snell.Obfs == "http" || n.Snell.Obfs == "tls" {
			user["obfsMode"], user["obfsHost"] = n.Snell.Obfs, n.Snell.ObfsHost
		}
		result["protocol"] = "snell"
		result["settings"] = map[string]any{"users": []any{user}}
	} else if n.Protocol == ProtocolMieru && n.Mieru != nil {
		// The fork's Mieru inbound listens on TCP and UDP of the port; clients
		// choose the underlay, so there is no transport key.
		result["protocol"] = "mieru"
		result["settings"] = map[string]any{"users": []any{map[string]any{"username": n.Mieru.Username, "password": n.Mieru.Password, "email": n.Tag()}}}
	}
	return result
}

func pemLines(data string) []string {
	return strings.Split(strings.TrimSpace(strings.ReplaceAll(data, "\r\n", "\n")), "\n")
}
