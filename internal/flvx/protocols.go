package flvx

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	extraSecret   = regexp.MustCompile(`^[A-Za-z0-9]{16,64}$`)
	mieruUsername = regexp.MustCompile(`^[A-Za-z0-9]{4,32}$`)
)

const (
	maxPEMBytes = 4 << 10
	// MaxBusyPorts bounds busy_ports and each reported listening list.
	MaxBusyPorts = 4096
	// Clock skew tolerated for a freshly generated AnyTLS certificate.
	certificateSkew = 10 * time.Minute
)

// DesiredOptions carries the landing-protocols-v1 facts of one share.
type DesiredOptions struct {
	// Protocols advertised for the share; nil rejects every extra protocol.
	Protocols map[string]ProtocolCap
	// Busy are ports other software listens on (never this share's nodes).
	Busy *ListeningPorts
	// Now, when set, also requires the AnyTLS certificate to be valid then.
	Now time.Time
}

// AllProtocols allows every extra protocol with the contract defaults, for
// structural checks where the share's capability is enforced elsewhere.
func AllProtocols() map[string]ProtocolCap {
	return map[string]ProtocolCap{ProtocolAnyTLS: {UDP: true}, ProtocolSnell: {UDP: true}, ProtocolMieru: {UDP: true}}
}

// UDPProtocol reports whether a node of protocol p also listens on UDP of
// its port: SS2022 and Mieru (one inbound serves both underlays).
func UDPProtocol(p string) bool { return p == "ss2022" || p == ProtocolMieru }

func (n Node) objects() int {
	count := 0
	for _, present := range []bool{n.SS2022 != nil, n.VLESS != nil, n.AnyTLS != nil, n.Snell != nil, n.Mieru != nil} {
		if present {
			count++
		}
	}
	return count
}

func validateExtra(n Node, capability ProtocolCap, now time.Time, add func(field, message string)) {
	object := map[string]bool{ProtocolAnyTLS: n.AnyTLS != nil, ProtocolSnell: n.Snell != nil, ProtocolMieru: n.Mieru != nil}
	if !object[n.Protocol] || n.objects() != 1 {
		add(n.Protocol, "fields of the node protocol are required exclusively")
		return
	}
	switch n.Protocol {
	case ProtocolAnyTLS:
		a := n.AnyTLS
		if !extraSecret.MatchString(a.Password) {
			add("anytls.password", "password must be 16–64 characters A–Z, a–z, 0–9")
		}
		if a.Padding != "default" {
			add("anytls.padding", "padding must be default")
		}
		if name := a.TLS.ServerName; name != "" && (!ValidHost(name) || net.ParseIP(name) != nil) {
			add("anytls.tls", "server_name must be empty or a DNS hostname")
		} else if !validAnyTLSCertificate(a.TLS.CertificatePEM, a.TLS.KeyPEM, now) {
			add("anytls.tls", "certificate and key must be one valid X.509 certificate and its matching private key")
		}
	case ProtocolSnell:
		s := n.Snell
		if !extraSecret.MatchString(s.PSK) {
			add("snell.psk", "psk must be 16–64 characters A–Z, a–z, 0–9")
		}
		versions := capability.Versions
		if len(versions) == 0 {
			versions = []int{4, 5}
		}
		if (s.Version != 4 && s.Version != 5) || !containsInt(versions, s.Version) {
			add("snell.version", "Snell version is not supported on this share")
		}
		obfs := capability.Obfs
		if len(obfs) == 0 {
			obfs = []string{"none", "http", "tls"}
		}
		switch {
		case (s.Obfs != "none" && s.Obfs != "http" && s.Obfs != "tls") || !containsString(obfs, s.Obfs):
			add("snell.obfs", "obfs mode is not supported on this share")
		case s.Obfs == "none" && s.ObfsHost != "":
			add("snell.obfs", "obfs_host must be empty without obfs")
		case s.Obfs != "none" && (!ValidHost(s.ObfsHost) || net.ParseIP(s.ObfsHost) != nil):
			add("snell.obfs", "obfs_host must be a DNS hostname")
		}
	case ProtocolMieru:
		m := n.Mieru
		if !mieruUsername.MatchString(m.Username) {
			add("mieru.username", "username must be 4–32 characters A–Z, a–z, 0–9")
		}
		if !extraSecret.MatchString(m.Password) {
			add("mieru.password", "password must be 16–64 characters A–Z, a–z, 0–9")
		}
	}
}

// singlePEM returns the only PEM block of data, nil when there is none, more
// than one, or anything besides whitespace around it.
func singlePEM(data string) *pem.Block {
	if len(data) == 0 || len(data) > maxPEMBytes {
		return nil
	}
	block, rest := pem.Decode([]byte(data))
	if block == nil || strings.TrimSpace(string(rest)) != "" {
		return nil
	}
	return block
}

func validAnyTLSCertificate(certificatePEM, keyPEM string, now time.Time) bool {
	cert, key := singlePEM(certificatePEM), singlePEM(keyPEM)
	if cert == nil || key == nil || cert.Type != "CERTIFICATE" || (key.Type != "PRIVATE KEY" && key.Type != "EC PRIVATE KEY") {
		return false
	}
	parsed, err := x509.ParseCertificate(cert.Bytes)
	if err != nil {
		return false
	}
	if !now.IsZero() && (now.Add(certificateSkew).Before(parsed.NotBefore) || now.After(parsed.NotAfter)) {
		return false
	}
	// X509KeyPair parses PKCS#8 / SEC 1 keys and checks they match the certificate.
	_, err = tls.X509KeyPair([]byte(certificatePEM), []byte(keyPEM))
	return err == nil
}

// SanitizeProtocols keeps the well-formed entries of known extra protocols,
// like FLVX's own Sanitize; nil when nothing usable remains.
func SanitizeProtocols(in map[string]ProtocolCap) map[string]ProtocolCap {
	out := map[string]ProtocolCap{}
	for _, p := range ExtraProtocols {
		c, ok := in[p]
		if !ok {
			continue
		}
		clean := ProtocolCap{UDP: c.UDP}
		switch p {
		case ProtocolSnell:
			for _, v := range c.Versions {
				if (v == 4 || v == 5) && !containsInt(clean.Versions, v) {
					clean.Versions = append(clean.Versions, v)
				}
			}
			for _, o := range c.Obfs {
				if (o == "none" || o == "http" || o == "tls") && !containsString(clean.Obfs, o) {
					clean.Obfs = append(clean.Obfs, o)
				}
			}
			if len(c.Versions) > 0 && len(clean.Versions) == 0 || len(c.Obfs) > 0 && len(clean.Obfs) == 0 {
				continue
			}
		case ProtocolMieru:
			for _, t := range c.Transports {
				if (t == "TCP" || t == "UDP") && !containsString(clean.Transports, t) {
					clean.Transports = append(clean.Transports, t)
				}
			}
			if len(c.Transports) > 0 && len(clean.Transports) == 0 {
				continue
			}
		}
		out[p] = clean
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Valid bounds a Helper's listening report.
func (l *ListeningPorts) Valid() bool {
	if l == nil {
		return true
	}
	for _, ports := range [][]int{l.TCP, l.UDP} {
		if len(ports) > MaxBusyPorts {
			return false
		}
		for _, p := range ports {
			if p < 1 || p > 65535 {
				return false
			}
		}
	}
	return true
}

// BusyPorts merges ports taken by anything but the share's own nodes inside
// [min, max]: sorted, unique, at most MaxBusyPorts (contract busy_ports).
func BusyPorts(min, max int, own map[int]bool, lists ...[]int) []int {
	seen := map[int]bool{}
	result := []int{}
	for _, list := range lists {
		for _, p := range list {
			if p < min || p > max || own[p] || seen[p] {
				continue
			}
			seen[p] = true
			result = append(result, p)
		}
	}
	sort.Ints(result)
	if len(result) > MaxBusyPorts {
		result = result[:MaxBusyPorts]
	}
	return result
}

func containsInt(list []int, v int) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func containsString(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
