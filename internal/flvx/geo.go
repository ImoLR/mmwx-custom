package flvx

import (
	"bufio"
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Pinned geo data (contract §5.5): the Core release ships none. Category codes
// and their protobuf entry sizes come from exactly these files; FLVX embeds the
// same table (go-backend/internal/landingegress/data/geo_codes.txt).
const GeoRelease = "202610182205"

var GeoSHA256 = map[string]string{
	"geoip.dat":   "010bfc2e353c5e34e23156004944444ca260bae999e73171bdd7974056557959",
	"geosite.dat": "3691de3874692fc8709686998c7627d030df3910d21f3d68232aaa0f1249dd2d",
}

var GeoSize = map[string]int64{"geoip.dat": 16587403, "geosite.dat": 11225745}

func GeoURL(name string) string {
	return "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/download/" + GeoRelease + "/" + name
}

//go:embed data/geo_codes.txt
var geoCodesRaw string

type geoInfo struct{ bytes, items int }

var geoOnce sync.Once
var geoIPTable, geoSiteTable map[string]geoInfo

func geoTables() (map[string]geoInfo, map[string]geoInfo) {
	geoOnce.Do(func() {
		geoIPTable, geoSiteTable = map[string]geoInfo{}, map[string]geoInfo{}
		for _, line := range strings.Split(geoCodesRaw, "\n") {
			f := strings.Fields(line)
			if len(f) != 4 || strings.HasPrefix(line, "#") {
				continue
			}
			b, _ := strconv.Atoi(f[2])
			n, _ := strconv.Atoi(f[3])
			if f[0] == "ip" {
				geoIPTable[f[1]] = geoInfo{b, n}
			} else {
				geoSiteTable[f[1]] = geoInfo{b, n}
			}
		}
	})
	return geoIPTable, geoSiteTable
}

// GeoEntry returns (known, protobuf bytes) for "geosite:x[@attr]", "geoip:x"
// or "geoip:!x". Non-geo entries report (true, 0).
func GeoEntry(entry string) (bool, int) {
	ip, site := geoTables()
	if code, ok := strings.CutPrefix(entry, "geosite:"); ok {
		code, _, _ = strings.Cut(code, "@")
		g, found := site[code]
		return found, g.bytes
	}
	if code, ok := strings.CutPrefix(entry, "geoip:"); ok {
		g, found := ip[strings.TrimPrefix(code, "!")]
		return found, g.bytes
	}
	return true, 0
}

// ReadGeoIP returns the CIDRs of one geoip.dat category, scanning entry by
// entry like the Core so only the requested category is decoded and held.
func ReadGeoIP(path, code string) ([]netip.Prefix, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	code = strings.ToUpper(code)
	br := bufio.NewReaderSize(f, 64<<10)
	var body []byte
	for {
		tag, err := readVarint(br)
		if err == io.EOF {
			return nil, fmt.Errorf("geoip:%s 不存在", strings.ToLower(code))
		}
		if err != nil {
			return nil, err
		}
		length, err := readVarint(br)
		if err != nil || tag != 0x0a || length > 64<<20 {
			return nil, errors.New("geoip.dat 格式无效")
		}
		if uint64(cap(body)) < length {
			body = make([]byte, length)
		}
		body = body[:length]
		if _, err := io.ReadFull(br, body); err != nil {
			return nil, err
		}
		entryCode, cidrs, err := parseGeoIPEntry(body, code)
		if err != nil {
			return nil, err
		}
		if entryCode == code {
			return cidrs, nil
		}
	}
}

func readVarint(r io.ByteReader) (uint64, error) {
	var x uint64
	for shift := uint(0); shift < 64; shift += 7 {
		b, err := r.ReadByte()
		if err != nil {
			if shift > 0 && err == io.EOF {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, err
		}
		x |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return x, nil
		}
	}
	return 0, errors.New("varint overflow")
}

// parseGeoIPEntry decodes GeoIP{country_code=1, repeated CIDR cidr=2{ip=1,
// prefix=2}}. CIDRs are decoded only for the wanted code (field 1 precedes the
// CIDRs in the pinned files; a later code still yields a correct result).
func parseGeoIPEntry(b []byte, want string) (string, []netip.Prefix, error) {
	r := bytes.NewReader(b)
	code := ""
	var raw [][]byte
	for r.Len() > 0 {
		key, err := readVarint(r)
		if err != nil {
			return "", nil, err
		}
		switch key & 7 {
		case 0:
			if _, err := readVarint(r); err != nil {
				return "", nil, err
			}
		case 2:
			n, err := readVarint(r)
			if err != nil || n > uint64(r.Len()) {
				return "", nil, errors.New("geoip.dat 格式无效")
			}
			start := len(b) - r.Len()
			v := b[start : start+int(n)]
			r.Seek(int64(n), io.SeekCurrent)
			switch key >> 3 {
			case 1:
				code = strings.ToUpper(string(v))
				if code != want {
					return code, nil, nil
				}
			case 2:
				raw = append(raw, v)
			}
		default:
			return "", nil, errors.New("geoip.dat 格式无效")
		}
	}
	if code != want {
		return code, nil, nil
	}
	out := make([]netip.Prefix, 0, len(raw))
	for _, v := range raw {
		p, err := parseGeoCIDR(v)
		if err != nil {
			return "", nil, err
		}
		out = append(out, p)
	}
	return code, out, nil
}

func parseGeoCIDR(b []byte) (netip.Prefix, error) {
	r := bytes.NewReader(b)
	var ip []byte
	prefix := -1
	for r.Len() > 0 {
		key, err := readVarint(r)
		if err != nil {
			return netip.Prefix{}, err
		}
		switch key {
		case 0x0a:
			n, err := readVarint(r)
			if err != nil || n > uint64(r.Len()) {
				return netip.Prefix{}, errors.New("geoip.dat CIDR 无效")
			}
			ip = make([]byte, n)
			io.ReadFull(r, ip)
		case 0x10:
			v, err := readVarint(r)
			if err != nil {
				return netip.Prefix{}, err
			}
			prefix = int(v)
		default:
			return netip.Prefix{}, errors.New("geoip.dat CIDR 无效")
		}
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok || prefix < 0 || prefix > addr.BitLen() {
		return netip.Prefix{}, errors.New("geoip.dat CIDR 无效")
	}
	if addr.Is4In6() && prefix >= 96 {
		return netip.PrefixFrom(addr.Unmap(), prefix-96).Masked(), nil
	}
	return netip.PrefixFrom(addr, prefix).Masked(), nil
}
