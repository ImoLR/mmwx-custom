package flvx

import (
	"fmt"
	"math/bits"
	"net/netip"
	"sort"
	"strings"
)

// u128 is an unsigned 128-bit integer used for both address families. IPv4
// values occupy the low 32 bits; the family is carried separately.
type u128 struct{ hi, lo uint64 }

func (a u128) cmp(b u128) int {
	switch {
	case a.hi < b.hi:
		return -1
	case a.hi > b.hi:
		return 1
	case a.lo < b.lo:
		return -1
	case a.lo > b.lo:
		return 1
	}
	return 0
}
func (a u128) add1() (u128, bool) {
	lo, carry := bits.Add64(a.lo, 1, 0)
	hi, overflow := bits.Add64(a.hi, 0, carry)
	return u128{hi, lo}, overflow != 0
}
func (a u128) sub1() u128 {
	lo, borrow := bits.Sub64(a.lo, 1, 0)
	hi, _ := bits.Sub64(a.hi, 0, borrow)
	return u128{hi, lo}
}
func (a u128) trailingZeros() int {
	if a.lo != 0 {
		return bits.TrailingZeros64(a.lo)
	}
	if a.hi != 0 {
		return 64 + bits.TrailingZeros64(a.hi)
	}
	return 128
}

// mask returns 2^n - 1.
func mask(n int) u128 {
	switch {
	case n <= 0:
		return u128{}
	case n < 64:
		return u128{0, 1<<uint(n) - 1}
	case n == 64:
		return u128{0, ^uint64(0)}
	case n < 128:
		return u128{1<<uint(n-64) - 1, ^uint64(0)}
	}
	return u128{^uint64(0), ^uint64(0)}
}
func (a u128) or(b u128) u128     { return u128{a.hi | b.hi, a.lo | b.lo} }
func (a u128) andNot(b u128) u128 { return u128{a.hi &^ b.hi, a.lo &^ b.lo} }

type ipRange struct{ lo, hi u128 }

// ipSet holds normalized (sorted, merged) ranges for one family.
type ipSet struct {
	bits   int // 32 or 128
	ranges []ipRange
}

func addrToU128(a netip.Addr) u128 {
	if a.Is4() {
		b := a.As4()
		return u128{0, uint64(b[0])<<24 | uint64(b[1])<<16 | uint64(b[2])<<8 | uint64(b[3])}
	}
	b := a.As16()
	var v u128
	for i := 0; i < 8; i++ {
		v.hi = v.hi<<8 | uint64(b[i])
		v.lo = v.lo<<8 | uint64(b[i+8])
	}
	return v
}
func u128ToAddr(v u128, bitsLen int) netip.Addr {
	if bitsLen == 32 {
		x := uint32(v.lo)
		return netip.AddrFrom4([4]byte{byte(x >> 24), byte(x >> 16), byte(x >> 8), byte(x)})
	}
	var b [16]byte
	for i := 7; i >= 0; i-- {
		b[i] = byte(v.hi)
		b[i+8] = byte(v.lo)
		v.hi >>= 8
		v.lo >>= 8
	}
	return netip.AddrFrom16(b)
}

// parsePrefix accepts a CIDR or a bare IP literal. IPv4-mapped IPv6 literals
// are normalized to IPv4, matching how the Core classifies source addresses.
func parsePrefix(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("invalid CIDR %q", s)
		}
		if p.Addr().Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid IP %q", s)
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func prefixRange(p netip.Prefix) (ipRange, int) {
	bitsLen := p.Addr().BitLen()
	lo := addrToU128(p.Addr())
	host := mask(bitsLen - p.Bits())
	return ipRange{lo.andNot(host), lo.or(host)}, bitsLen
}

func newIPSet(bitsLen int, ranges []ipRange) ipSet {
	s := ipSet{bits: bitsLen}
	if len(ranges) == 0 {
		return s
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].lo.cmp(ranges[j].lo) < 0 })
	cur := ranges[0]
	for _, r := range ranges[1:] {
		next, overflow := cur.hi.add1()
		if overflow || r.lo.cmp(next) <= 0 {
			if r.hi.cmp(cur.hi) > 0 {
				cur.hi = r.hi
			}
			continue
		}
		s.ranges = append(s.ranges, cur)
		cur = r
	}
	s.ranges = append(s.ranges, cur)
	return s
}

func fullSet(bitsLen int) ipSet {
	return ipSet{bits: bitsLen, ranges: []ipRange{{u128{}, mask(bitsLen)}}}
}

// subtract returns s minus o (both normalized, same family).
func (s ipSet) subtract(o ipSet) ipSet {
	out := ipSet{bits: s.bits}
	j := 0
	for _, r := range s.ranges {
		lo, hi := r.lo, r.hi
		alive := true
		for j < len(o.ranges) && o.ranges[j].hi.cmp(lo) < 0 {
			j++
		}
		for k := j; k < len(o.ranges) && alive; k++ {
			x := o.ranges[k]
			if x.lo.cmp(hi) > 0 {
				break
			}
			if x.lo.cmp(lo) > 0 {
				out.ranges = append(out.ranges, ipRange{lo, x.lo.sub1()})
			}
			if x.hi.cmp(hi) >= 0 {
				alive = false
				break
			}
			lo, _ = x.hi.add1()
		}
		if alive {
			out.ranges = append(out.ranges, ipRange{lo, hi})
		}
	}
	return out
}

func (s ipSet) intersects(o ipSet) bool {
	i, j := 0, 0
	for i < len(s.ranges) && j < len(o.ranges) {
		a, b := s.ranges[i], o.ranges[j]
		if a.hi.cmp(b.lo) < 0 {
			i++
		} else if b.hi.cmp(a.lo) < 0 {
			j++
		} else {
			return true
		}
	}
	return false
}

// cidrs converts ranges back to the minimal exact CIDR cover.
func (s ipSet) cidrs() []string {
	out := []string{}
	top := mask(s.bits)
	for _, r := range s.ranges {
		lo := r.lo
		for {
			size := lo.trailingZeros()
			if size > s.bits {
				size = s.bits
			}
			for size > 0 && lo.or(mask(size)).cmp(r.hi) > 0 {
				size--
			}
			end := lo.or(mask(size))
			out = append(out, netip.PrefixFrom(u128ToAddr(lo, s.bits), s.bits-size).String())
			if end.cmp(r.hi) >= 0 || end.cmp(top) == 0 {
				break
			}
			lo, _ = end.add1()
		}
	}
	return out
}

// dualSet groups both families so callers can treat source lists uniformly.
type dualSet struct{ v4, v6 ipSet }

func newDualSet(prefixes []netip.Prefix) dualSet {
	var r4, r6 []ipRange
	for _, p := range prefixes {
		r, bitsLen := prefixRange(p)
		if bitsLen == 32 {
			r4 = append(r4, r)
		} else {
			r6 = append(r6, r)
		}
	}
	return dualSet{newIPSet(32, r4), newIPSet(128, r6)}
}
func (d dualSet) union(o dualSet) dualSet {
	return dualSet{newIPSet(32, append(append([]ipRange{}, d.v4.ranges...), o.v4.ranges...)), newIPSet(128, append(append([]ipRange{}, d.v6.ranges...), o.v6.ranges...))}
}
func (d dualSet) subtract(o dualSet) dualSet {
	return dualSet{d.v4.subtract(o.v4), d.v6.subtract(o.v6)}
}
func (d dualSet) complement() dualSet {
	return dualSet{fullSet(32).subtract(d.v4), fullSet(128).subtract(d.v6)}
}
func (d dualSet) intersects(o dualSet) bool {
	return d.v4.intersects(o.v4) || d.v6.intersects(o.v6)
}
func (d dualSet) empty() bool { return len(d.v4.ranges) == 0 && len(d.v6.ranges) == 0 }
func (d dualSet) cidrs() []string {
	return append(d.v4.cidrs(), d.v6.cidrs()...)
}
