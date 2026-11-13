package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"time"

	"github.com/ImoLR/mmwx-custom/internal/flvx"
)

// Contract landing-protocols-v1 on the Helper. The running FLVX Core's
// /v1/features gate what the share advertises (§7): a Core without the
// endpoint predates the AnyTLS downlink and Snell UDP fixes, so nothing is
// advertised and no extra-protocol node is rendered there. The machine's
// listening ports inside the share range become busy_ports (§2).
const (
	flvxProtocolsCapability = "flvx.protocols.v1"
	coreFeatureAnyTLS       = "anytls.downlink_stats"
	coreFeatureSnellUDP     = "snell.udp_per_packet_target"
	// Not a /v1/features name: the Helper's own config test of the build
	// (vlessEncSupported), so VLESS Encryption is advertised only where the
	// Core accepts exactly the inbound Inbound renders.
	coreFeatureVLESSEnc  = "mmwxc.vless_enc"
	flvxListeningRefresh = 30 * time.Second
)

// flvxCoreFeatures are the features of the FLVX Core build SHA256 (the
// installed artifact), kept across Helper restarts so the capability does
// not flap while the Core is briefly unreachable. VLESSEnc is the config
// test result for that build; nil = not tested yet (or the test could not
// run), retried on the next Core start or Helper start.
type flvxCoreFeatures struct {
	SHA256   string   `json:"sha256"`
	Features []string `json:"features"`
	VLESSEnc *bool    `json:"vless_enc,omitempty"`
}

// features reads /v1/features. A Core without the endpoint has none.
func (client *coreClient) features(ctx context.Context) ([]string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://mmwxc-core/v1/features", nil)
	if err != nil {
		return nil, err
	}
	response, err := client.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return []string{}, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("core features HTTP %d", response.StatusCode)
	}
	var body struct {
		Features []string `json:"features"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode core features: %w", err)
	}
	if body.Features == nil {
		body.Features = []string{}
	}
	return body.Features, nil
}

// detectFeatures asks a newly started Core (new epoch) once for its features.
func (m *flvxManager) detectFeatures(ctx context.Context, epoch string) {
	if m.featuresEpoch == epoch && m.state.CoreFeatures != nil && m.state.CoreFeatures.SHA256 == m.state.Payload.Artifact.SHA256 {
		return
	}
	read := m.readFeatures
	if read == nil {
		read = m.core.features
	}
	features, err := read(ctx)
	m.logs.report("FLVX Core features", err)
	if err != nil {
		return
	}
	sha := m.state.Payload.Artifact.SHA256
	next := &flvxCoreFeatures{SHA256: sha, Features: features}
	if previous := m.state.CoreFeatures; previous != nil && previous.SHA256 == sha {
		next.VLESSEnc = previous.VLESSEnc
	}
	// Once per Core build: a Core without /v1/features gets no extra
	// protocol anyway.
	if next.VLESSEnc == nil && len(features) > 0 {
		test := m.testVLESSEnc
		if test == nil {
			test = m.vlessEncSupported
		}
		supported, err := test(ctx)
		m.logs.report("FLVX Core VLESS Encryption check", err)
		if err == nil {
			next.VLESSEnc = &supported
		}
	}
	m.featuresEpoch = epoch
	m.state.CoreFeatures = next
}

// vlessEncSupported runs the installed Core once with -test on a config
// holding one rendered VLESS Encryption inbound (throwaway key, never
// started, nothing bound). false = the build rejects it; err = the test
// itself could not run (timeout, signal, exec failure), so nothing is known.
func (m *flvxManager) vlessEncSupported(ctx context.Context) (bool, error) {
	key := make([]byte, 32)
	id := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return false, err
	}
	if _, err := rand.Read(id); err != nil {
		return false, err
	}
	id[6], id[8] = id[6]&0x0f|0x40, id[8]&0x3f|0x80
	node := flvx.Node{ID: "vless-enc-check", Protocol: flvx.ProtocolVLESSEnc, Port: 1, VLESSEnc: &flvx.VLESSEnc{
		UUID: fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]), Flow: "xtls-rprx-vision",
		Decryption: "mlkem768x25519plus.random.600s." + base64.RawURLEncoding.EncodeToString(key)}}
	config, _ := json.Marshal(map[string]any{"log": map[string]any{"loglevel": "none"}, "inbounds": []any{flvx.Inbound(node)}, "outbounds": []any{map[string]any{"protocol": "freedom"}}})
	child, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(child, m.binary, "run", "-test", "-config", "stdin:")
	command.Dir = "/"
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	command.Stdin = bytes.NewReader(config)
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if child.Err() == nil && errors.As(err, &exit) && exit.ExitCode() > 0 {
		return false, nil
	}
	return false, fmt.Errorf("FLVX Core config test did not run: %w", err)
}

// coreFeatures is the feature set of the installed FLVX Core build; nil when
// it is unknown or predates /v1/features.
func (m *flvxManager) coreFeatures() map[string]bool {
	f := m.state.CoreFeatures
	if f == nil || f.SHA256 == "" || f.SHA256 != m.state.Payload.Artifact.SHA256 || len(f.Features) == 0 {
		return nil
	}
	set := map[string]bool{}
	for _, name := range f.Features {
		set[name] = true
	}
	if f.VLESSEnc != nil && *f.VLESSEnc {
		set[coreFeatureVLESSEnc] = true
	}
	return set
}

// flvxProtocolCaps is the flvx_protocols report for a Core's features.
func flvxProtocolCaps(features map[string]bool) map[string]flvx.ProtocolCap {
	if len(features) == 0 {
		return nil
	}
	caps := map[string]flvx.ProtocolCap{
		flvx.ProtocolSnell: {UDP: features[coreFeatureSnellUDP], Versions: []int{4, 5}, Obfs: []string{"none", "http", "tls"}},
		flvx.ProtocolMieru: {UDP: true, Transports: []string{"TCP", "UDP"}},
	}
	if features[coreFeatureAnyTLS] {
		caps[flvx.ProtocolAnyTLS] = flvx.ProtocolCap{UDP: true}
	}
	// UDP rides the VLESS stream (XUDP), see Inbound.
	if features[coreFeatureVLESSEnc] {
		caps[flvx.ProtocolVLESSEnc] = flvx.ProtocolCap{UDP: true, Versions: []int{flvx.VLESSEncVersion}}
	}
	return caps
}

// flvxProtocolProblem says why a node of protocol p must not be rendered into
// this Core; "" when it may.
func flvxProtocolProblem(p string, features map[string]bool) string {
	if !flvx.IsExtraProtocol(p) {
		return ""
	}
	if _, ok := flvxProtocolCaps(features)[p]; !ok {
		if p == flvx.ProtocolVLESSEnc {
			return "FLVX Core does not support VLESS Encryption; upgrade the FLVX Core"
		}
		return "FLVX Core lacks the fix this protocol needs; upgrade the FLVX Core"
	}
	return ""
}

// listening reports the machine's TCP LISTEN and unconnected UDP ports
// inside the share range, minus this Core's own inbounds. It rescans at most
// every flvxListeningRefresh, and at once when the range or this Core's own
// inbounds change, so a removed node's port is never reported as taken.
func (m *flvxManager) listening() *flvx.ListeningPorts {
	c := m.state.Payload.Config
	if c.PortMin < 1 || c.PortMax < c.PortMin {
		return nil
	}
	own := m.appliedPorts()
	key := fmt.Sprint(c.PortMin, c.PortMax, own.TCP, own.UDP)
	if m.listeningCache != nil && m.now().Sub(m.listeningAt) < flvxListeningRefresh && m.listeningKey == key {
		return m.listeningCache
	}
	read := m.readListening
	if read == nil {
		read = readListeningSockets
	}
	tcp, udp, err := read()
	m.logs.report("FLVX listening port scan", err)
	if err != nil {
		return m.listeningCache
	}
	result := &flvx.ListeningPorts{TCP: flvxPortsInRange(tcp, own.TCP, c), UDP: flvxPortsInRange(udp, own.UDP, c)}
	m.listeningCache, m.listeningAt, m.listeningKey = result, m.now(), key
	return result
}

func flvxPortsInRange(ports, own []uint32, c flvx.Config) []int {
	skip := map[uint32]bool{}
	for _, p := range own {
		skip[p] = true
	}
	seen := map[int]bool{}
	result := []int{}
	for _, p := range ports {
		port := int(p)
		if port < c.PortMin || port > c.PortMax || skip[p] || seen[port] {
			continue
		}
		seen[port] = true
		result = append(result, port)
	}
	sort.Ints(result)
	if len(result) > flvx.MaxBusyPorts {
		result = result[:flvx.MaxBusyPorts]
	}
	return result
}

// readListeningSockets returns every TCP LISTEN port and every unconnected
// (bound) UDP port of any owner and address, loopback included: a wildcard
// bind of the FLVX Core collides with all of them.
func readListeningSockets() (tcp, udp []uint32, err error) {
	sockets, err := readListenerSockets("/proc")
	if err != nil {
		return nil, nil, err
	}
	for _, socket := range sockets {
		if socket.Protocol == "udp" {
			udp = append(udp, socket.Port)
		} else {
			tcp = append(tcp, socket.Port)
		}
	}
	return tcp, udp, nil
}
