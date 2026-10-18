package flvx

import "strconv"

func BaseConfig(apiPort int) map[string]any {
	return map[string]any{
		"log":       map[string]any{"loglevel": "warning", "access": "none"},
		"api":       map[string]any{"tag": "flvx-api", "listen": "127.0.0.1:" + strconv.Itoa(apiPort), "services": []string{"HandlerService", "StatsService"}},
		"stats":     map[string]any{},
		"policy":    map[string]any{"system": map[string]any{"statsInboundUplink": true, "statsInboundDownlink": true}},
		"inbounds":  []any{},
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom", "settings": map[string]any{"domainStrategy": "UseIP"}}, map[string]any{"tag": "block", "protocol": "blackhole"}},
		"routing":   map[string]any{"domainStrategy": "IPOnDemand", "rules": []any{map[string]any{"type": "field", "ip": BlockCIDRs, "outboundTag": "block"}}},
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
	}
	return result
}
