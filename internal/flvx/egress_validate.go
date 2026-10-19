package flvx

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

var (
	outboundTagRe = regexp.MustCompile(`^flvxo-[a-z0-9]{1,32}$`)
	geoCodeRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9!_.-]{0,63}$`)
	geoAttrRe     = regexp.MustCompile(`^[a-z0-9!_-]{1,32}$`)
	domainValueRe = regexp.MustCompile(`^[a-z0-9_*.-]{1,253}$`)
	hostnameRe    = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62})(\.[a-z0-9]([a-z0-9-]{0,62}))*\.?$`)
	shortIDRe     = regexp.MustCompile(`^[0-9a-f]{0,16}$`)
)

var egressIPVersions = map[string]bool{"system": true, "prefer_ipv4": true, "prefer_ipv6": true, "force_ipv4": true, "force_ipv6": true}
var egressProtocols = map[string]bool{"http": true, "tls": true, "quic": true, "bittorrent": true}
var ss2022KeySize = map[string]int{"2022-blake3-aes-128-gcm": 16, "2022-blake3-aes-256-gcm": 32, "2022-blake3-chacha20-poly1305": 32}
var ssAEADMethods = map[string]bool{"aes-128-gcm": true, "aes-256-gcm": true, "chacha20-ietf-poly1305": true, "xchacha20-ietf-poly1305": true}
var realityFingerprints = map[string]bool{"chrome": true, "firefox": true, "safari": true, "ios": true, "android": true, "edge": true, "random": true, "randomized": true}

// BlockedAddr reports the §4.4 private/loopback/link-local/metadata/
// multicast/reserved destinations (IPv4-mapped forms included).
func BlockedAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsUnspecified() {
		return true
	}
	for _, cidr := range BlockCIDRs {
		if netip.MustParsePrefix(cidr).Contains(a) {
			return true
		}
	}
	return false
}

// validUpstreamHost accepts public IP literals and dotted lowercase hostnames.
func validUpstreamHost(h string) bool {
	if a, err := netip.ParseAddr(h); err == nil {
		return !BlockedAddr(a)
	}
	return len(h) <= 253 && hostnameRe.MatchString(h) && strings.Contains(strings.TrimSuffix(h, "."), ".")
}

func decodeKey32(s string, rawURL bool) bool {
	var b []byte
	var err error
	if rawURL {
		b, err = base64.RawURLEncoding.Strict().DecodeString(s)
	} else {
		b, err = base64.StdEncoding.Strict().DecodeString(s)
	}
	return err == nil && len(b) == 32
}

func validPortSpec(spec string) (int, bool) {
	if spec == "" {
		return 0, true
	}
	parts := strings.Split(spec, ",")
	if len(parts) > 64 {
		return 0, false
	}
	for _, part := range parts {
		lo, hi, ranged := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil || a < 1 || a > 65535 || strconv.Itoa(a) != lo {
			return 0, false
		}
		if ranged {
			b, err := strconv.Atoi(hi)
			if err != nil || b < a || b > 65535 || strconv.Itoa(b) != hi {
				return 0, false
			}
		}
	}
	return len(parts), true
}

func validDomainEntry(v string, geosite bool) string {
	kind, value, ok := strings.Cut(v, ":")
	if !ok || value == "" {
		return "域名条目需要 geosite:/domain:/full:/keyword:/regexp: 前缀"
	}
	switch kind {
	case "geosite":
		code, attr, hasAttr := strings.Cut(value, "@")
		if !geoCodeRe.MatchString(code) || hasAttr && !geoAttrRe.MatchString(attr) {
			return "geosite 分类无效"
		}
		if !geosite {
			return "该服务器的 Helper 不能提供 geosite 数据"
		}
		if known, _ := GeoEntry("geosite:" + code); !known {
			return fmt.Sprintf("geosite:%s 不存在（geo 数据版本 %s）", code, GeoRelease)
		}
	case "domain", "full":
		if !domainValueRe.MatchString(value) || strings.Contains(value, "*") {
			return "域名无效"
		}
	case "keyword":
		if len(value) > 128 || strings.ContainsAny(value, " \t\r\n\"") {
			return "关键字无效"
		}
	case "regexp":
		if len(value) > 256 {
			return "正则最长 256 个字符"
		}
		if _, err := regexp.Compile(value); err != nil {
			return "正则无效"
		}
	default:
		// ext: and every other prefix would make the Core CLI read files.
		return "域名条目只支持 geosite:/domain:/full:/keyword:/regexp:"
	}
	return ""
}

func validIPEntry(v string, geoip bool) string {
	if code, ok := strings.CutPrefix(v, "geoip:"); ok {
		code = strings.TrimPrefix(code, "!")
		if !geoCodeRe.MatchString(code) || strings.Contains(code, "!") {
			return "geoip 分类无效"
		}
		if !geoip {
			return "该服务器的 Helper 不能提供 geoip 数据"
		}
		if known, _ := GeoEntry("geoip:" + code); !known {
			return fmt.Sprintf("geoip:%s 不存在（geo 数据版本 %s）", code, GeoRelease)
		}
		return ""
	}
	if _, err := parsePrefix(v); err != nil {
		return "IP/CIDR 无效（只支持 geoip:、CIDR 和 IP）"
	}
	return ""
}

func forcedConflict(ipVersion, host string) bool {
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	return ipVersion == "force_ipv4" && !a.Is4() || ipVersion == "force_ipv6" && a.Is4()
}

func validateOutbound(o EgressOutbound, add func(string, string)) {
	if !egressIPVersions[o.IPVersion] {
		add("ip_version", "IP 版本只支持 system / prefer_ipv4 / prefer_ipv6 / force_ipv4 / force_ipv6")
	}
	proxy := func() {
		if !validUpstreamHost(o.Server) {
			add("server", "上游必须是公网 IP 或域名，不能是内网、回环或保留地址")
		} else if forcedConflict(o.IPVersion, o.Server) {
			add("server", "上游 IP 与强制 IP 版本不一致")
		}
		if o.Port < 1 || o.Port > 65535 {
			add("port", "端口必须在 1–65535 内")
		}
		if o.WireGuard != nil {
			add("wireguard", "只有 wireguard 出站可以带 wireguard 字段")
		}
	}
	credentials := func(s string) bool {
		return len(s) <= 255 && !strings.ContainsAny(s, "\r\n\x00")
	}
	switch o.Type {
	case "direct":
		if o.Server != "" || o.Port != 0 || o.Username != "" || o.Password != "" || o.Method != "" || o.UUID != "" || o.Flow != "" || o.Reality != nil || o.WireGuard != nil {
			add("type", "direct 出站不能带上游字段")
		}
	case "socks", "http":
		proxy()
		if !credentials(o.Username) || !credentials(o.Password) {
			add("password", "用户名或密码无效（最多 255 字节，不能含换行或 NUL）")
		}
		if o.Method != "" || o.UUID != "" || o.Flow != "" || o.Reality != nil {
			add("type", o.Type+" 出站带了其他协议的字段")
		}
	case "shadowsocks":
		proxy()
		if size, ok := ss2022KeySize[o.Method]; ok {
			parts := strings.Split(o.Password, ":")
			valid := len(parts) <= 2
			for _, p := range parts {
				b, err := base64.StdEncoding.Strict().DecodeString(p)
				valid = valid && err == nil && len(b) == size
			}
			if !valid {
				add("password", fmt.Sprintf("Shadowsocks 2022 密钥需为 %d 字节 Base64（可写成 iPSK:uPSK）", size))
			}
		} else if !ssAEADMethods[o.Method] {
			add("method", "Shadowsocks 加密方式不受支持")
		} else if o.Password == "" || len(o.Password) > 256 || strings.ContainsAny(o.Password, "\r\n\x00") {
			add("password", "Shadowsocks 密码无效")
		}
		if o.Username != "" || o.UUID != "" || o.Flow != "" || o.Reality != nil {
			add("type", "shadowsocks 出站带了其他协议的字段")
		}
	case "vless":
		proxy()
		if !uuid.MatchString(o.UUID) {
			add("uuid", "VLESS UUID 无效")
		}
		if o.Flow != "" && o.Flow != "xtls-rprx-vision" {
			add("flow", "flow 只能为空或 xtls-rprx-vision")
		}
		if r := o.Reality; r == nil {
			add("reality", "VLESS 出站必须使用 Reality")
		} else {
			if !hostnameRe.MatchString(r.ServerName) || net.ParseIP(r.ServerName) != nil || len(r.ServerName) > 253 {
				add("reality.server_name", "Reality server_name 必须是域名")
			}
			if !decodeKey32(r.PublicKey, true) {
				add("reality.public_key", "Reality 公钥需为 32 字节 base64url")
			}
			if !shortIDRe.MatchString(r.ShortID) || len(r.ShortID)%2 != 0 {
				add("reality.short_id", "Reality short_id 需为偶数长度、最多 16 位的十六进制")
			}
			if r.Fingerprint != "" && !realityFingerprints[r.Fingerprint] {
				add("reality.fingerprint", "Reality 指纹不受支持")
			}
		}
		if o.Username != "" || o.Password != "" || o.Method != "" {
			add("type", "vless 出站带了其他协议的字段")
		}
	case "wireguard":
		w := o.WireGuard
		if w == nil {
			add("wireguard", "wireguard 出站缺少 wireguard 字段")
			return
		}
		if o.Server != "" || o.Port != 0 || o.Username != "" || o.Password != "" || o.Method != "" || o.UUID != "" || o.Flow != "" || o.Reality != nil {
			add("type", "wireguard 出站只能使用 wireguard 字段")
		}
		if !decodeKey32(w.PrivateKey, false) {
			add("wireguard.private_key", "WireGuard 私钥需为 32 字节 Base64")
		}
		if !decodeKey32(w.PeerPublicKey, false) {
			add("wireguard.peer_public_key", "WireGuard 对端公钥需为 32 字节 Base64")
		}
		v4, v6 := false, false
		if w.AddressV4 != "" {
			if p, err := parsePrefix(w.AddressV4); err != nil || !p.Addr().Is4() {
				add("wireguard.address_v4", "WireGuard IPv4 接口地址无效")
			} else {
				v4 = true
			}
		}
		if w.AddressV6 != "" {
			if p, err := parsePrefix(w.AddressV6); err != nil || p.Addr().Is4() {
				add("wireguard.address_v6", "WireGuard IPv6 接口地址无效")
			} else {
				v6 = true
			}
		}
		if !v4 && !v6 {
			add("wireguard.address_v4", "至少需要一个 WireGuard 接口地址")
		}
		if o.IPVersion == "force_ipv4" && w.AddressV4 == "" {
			add("wireguard.address_v4", "强制 IPv4 需要 IPv4 接口地址")
		}
		if o.IPVersion == "force_ipv6" && w.AddressV6 == "" {
			add("wireguard.address_v6", "强制 IPv6 需要 IPv6 接口地址")
		}
		if len(w.Reserved) != 0 && len(w.Reserved) != 3 {
			add("wireguard.reserved", "reserved 需为 3 个 0–255 的数字")
		}
		for _, b := range w.Reserved {
			if b < 0 || b > 255 {
				add("wireguard.reserved", "reserved 需为 3 个 0–255 的数字")
				break
			}
		}
		if w.MTU != 0 && (w.MTU < 1280 || w.MTU > 1500) {
			add("wireguard.mtu", "MTU 需为 0 或 1280–1500")
		}
		for i, ep := range []string{w.Endpoint, w.EndpointV4, w.EndpointV6} {
			field := []string{"wireguard.endpoint", "wireguard.endpoint_v4", "wireguard.endpoint_v6"}[i]
			if ep == "" {
				continue
			}
			host, port, err := net.SplitHostPort(ep)
			p, perr := strconv.Atoi(port)
			if err != nil || perr != nil || p < 1 || p > 65535 || !validUpstreamHost(host) {
				add(field, "WireGuard 端点必须是公网 host:port，不能是内网、回环或保留地址")
				continue
			}
			// The forced family is the one inside the tunnel; the endpoint
			// family follows the server's own connectivity.
			a, literal := netip.ParseAddr(host)
			if i == 1 && (literal != nil || !a.Unmap().Is4()) || i == 2 && (literal != nil || a.Unmap().Is4()) {
				add(field, "endpoint_v4 / endpoint_v6 必须是对应协议族的 IP")
			}
		}
	default:
		add("type", "出站类型只支持 direct / wireguard / socks / http / shadowsocks / vless")
	}
}

// ValidateEgress repeats every check of contract §4. nodeTags is the set of
// inbound tags of this share's desired state; geoip/geosite are the Helper's
// reported geo capability. All errors are reported (node_id empty).
func ValidateEgress(e *Egress, nodeTags map[string]bool, geoip, geosite bool) []ValidationError {
	errs := []ValidationError{}
	if e == nil {
		return errs
	}
	add := func(field, message string) { errs = append(errs, ValidationError{Field: field, Message: message}) }
	if e.Version != EgressVersion {
		add("egress.version", "只支持出站与路由方案版本 1")
		return errs
	}
	if len(e.Outbounds) > MaxEgressOutbounds {
		add("egress.outbounds", fmt.Sprintf("同一服务器最多 %d 个出站", MaxEgressOutbounds))
	}
	if len(e.Rules) > MaxEgressRules {
		add("egress.rules", fmt.Sprintf("同一服务器最多 %d 条规则", MaxEgressRules))
	}
	if len(e.ExemptSources) > MaxEgressExempt {
		add("egress.exempt_sources", fmt.Sprintf("来源豁免地址最多 %d 条", MaxEgressExempt))
	}
	if len(errs) > 0 {
		return errs
	}
	tags := map[string]bool{"direct": true, "block": true}
	for i, o := range e.Outbounds {
		path := fmt.Sprintf("egress.outbounds[%d]", i)
		if !outboundTagRe.MatchString(o.Tag) {
			add(path+".tag", "出站标识必须匹配 flvxo-[a-z0-9]{1,32}")
		} else if tags[o.Tag] {
			add(path+".tag", "出站标识重复")
		}
		tags[o.Tag] = true
		validateOutbound(o, func(field, message string) { add(path+"."+field, o.Tag+"："+message) })
	}
	items, planGeo := 0, 0
	for i, r := range e.Rules {
		path := fmt.Sprintf("egress.rules[%d]", i)
		if len(r.InboundTags) == 0 {
			add(path+".inbound_tags", "规则必须指定适用的节点")
		}
		if len(r.InboundTags) > MaxNodes {
			add(path+".inbound_tags", "适用节点过多")
		} else {
			for j, t := range r.InboundTags {
				if !nodeTags[t] {
					add(fmt.Sprintf("%s.inbound_tags[%d]", path, j), "节点不属于这次提交的共享服务器节点")
				}
			}
		}
		if !tags[r.Outbound] {
			add(path+".outbound", "出站不存在")
		}
		if len(r.Name) > 256 {
			add(path+".name", "规则名称过长")
		}
		ruleGeo := 0
		for j, d := range r.Domain {
			if msg := validDomainEntry(d, geosite); msg != "" {
				add(fmt.Sprintf("%s.domain[%d]", path, j), msg)
			}
			_, b := GeoEntry(d)
			ruleGeo += b
		}
		for _, list := range []struct {
			field   string
			entries []string
		}{{"ip", r.IP}, {"source_ip", r.SourceIP}} {
			for j, v := range list.entries {
				if msg := validIPEntry(v, geoip); msg != "" {
					add(fmt.Sprintf("%s.%s[%d]", path, list.field, j), msg)
				}
				_, b := GeoEntry(v)
				ruleGeo += b
			}
		}
		ports, ok := validPortSpec(r.Port)
		if !ok {
			add(path+".port", "端口格式应为 443,8000-9000（最多 64 段）")
		}
		sourcePorts, ok := validPortSpec(r.SourcePort)
		if !ok {
			add(path+".source_port", "端口格式应为 443,8000-9000（最多 64 段）")
		}
		switch r.Network {
		case "", "tcp", "udp", "tcp,udp":
		default:
			add(path+".network", "网络只能是 tcp、udp 或 tcp,udp")
		}
		for j, p := range r.Protocol {
			if !egressProtocols[p] {
				add(fmt.Sprintf("%s.protocol[%d]", path, j), "协议只支持 http、tls、quic、bittorrent")
			}
		}
		if ruleGeo > MaxRuleGeoBytes {
			add(path, "这条规则引用的 geo 分类过大（超过 2.5 MiB），请改用更小的分类，例如 geosite:category-ads")
		}
		planGeo += ruleGeo
		items += len(r.Domain) + len(r.IP) + len(r.SourceIP) + len(r.Protocol) + ports + sourcePorts
	}
	if items > MaxEgressItems {
		add("egress.rules", fmt.Sprintf("匹配条目合计最多 %d 条", MaxEgressItems))
	}
	if planGeo > MaxPlanGeoBytes {
		add("egress.rules", "geo 分类合计过大（超过 3 MiB）")
	}
	for i, s := range e.ExemptSources {
		if _, err := parsePrefix(s); err != nil {
			add(fmt.Sprintf("egress.exempt_sources[%d]", i), "来源豁免地址必须是 IP 或 CIDR")
		}
	}
	return errs
}
