package main

import (
	"strings"
	"testing"
)

func TestParseProcNetTCPIPv4AndStates(t *testing.T) {
	fixture := `  sl  local_address rem_address   st
   0: 0100007F:1F90 0200007F:C350 01 00000000:00000000 00:00000000 00000000
   1: 00000000:01BB 00000000:0000 0A 00000000:00000000 00:00000000 00000000
`
	entries, err := parseProcNetTCP(strings.NewReader(fixture), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if got := entries[0]; got.LocalIP.String() != "127.0.0.1" || got.RemoteIP.String() != "127.0.0.2" || got.LocalPort != 8080 || got.State != tcpEstablished {
		t.Fatalf("unexpected first entry: %#v", got)
	}
	counts := summarizeTCP(entries)
	if counts.Total != 2 || counts.Established != 1 {
		t.Fatalf("unexpected counts: %#v", counts)
	}
}

func TestParseProcNetTCPIPv6AndNormalizePrefix(t *testing.T) {
	fixture := `  sl  local_address rem_address   st
   0: 00000000000000000000000000000000:1F90 B80D01203412CDAB0000000001000000:C350 01 00000000:00000000 00:00000000 00000000
`
	entries, err := parseProcNetTCP(strings.NewReader(fixture), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := entries[0].RemoteIP.String(); got != "2001:db8:abcd:1234::1" {
		t.Fatalf("IPv6 = %s", got)
	}
	if got := normalizeOnlineIP(entries[0].RemoteIP); got != "2001:db8:abcd:1234::/64" {
		t.Fatalf("normalized IPv6 = %s", got)
	}
}
