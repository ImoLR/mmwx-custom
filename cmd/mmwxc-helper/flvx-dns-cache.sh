#!/bin/bash
# flvx-dns-cache — local DNS cache for an FLVX node (plan 240; the plan-239 ops recipe of 2026-11-08 as a product).
#
#   flvx-dns-cache ensure [--zones "a.com b.net"] [--dry-run]  set up / repair; idempotent, no-op when already correct
#   flvx-dns-cache status                                       human-readable state (read-only)
#   flvx-dns-cache check                                        one line "mode=.. ok=.. reason=.." (same rules as the agent)
#   flvx-dns-cache rollback [all|b]                             all = DNS config from before the first change
#                                                               b   = drop only the local cache, keep 2nd resolver + timeout
# Modes
#   dnsmasq  (default) dnsmasq-base binary + own unit flvx-dnscache, 127.0.0.1/::1 only, all-servers, cache 1000
#   resolved systemd-resolved already runs its stub (127.0.0.53) -> use it, make sure it has >= 2 upstreams
#   minimal  low RAM / :53 held by another program / no verified package -> only >= 2 resolvers + timeout:1 attempts:2
# ensure never leaves the host without DNS: every change is checked, and a failed cache falls back to minimal.
# Exit code of ensure: 0 = ended in a working mode (incl. minimal), 1 = could not make DNS consistent (see output).
# Files: /etc/flvx-dnscache/{dnsmasq.conf,state}, /etc/systemd/system/flvx-dnscache.service,
#        backups + rollback.sh in /root/dns-backup-<date>/ (same layout as plan 239, which it reuses when present).

set -u
umask 022
export LC_ALL=C
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
SCRIPT_VERSION=1

DIR=/etc/flvx-dnscache
CONF=$DIR/dnsmasq.conf
STATE=$DIR/state
UNIT=flvx-dnscache.service
UNIT_FILE=/etc/systemd/system/$UNIT
LOCK=/run/flvx-dns-cache.lock
RESOLVED_DROPIN=/etc/systemd/resolved.conf.d/90-flvx-dns.conf
DHCLIENT_HOOK=/etc/dhcp/dhclient-enter-hooks.d/zz-flvx-dns
OPTS_WANT="timeout:1 attempts:2"
PUBLIC4="1.1.1.1 8.8.4.4"
PUBLIC6="2606:4700:4700::1111 2001:4860:4860::8844"
OURNS_DEFAULT="1.1.1.1 1.0.0.1"
CN_PUBLIC=223.5.5.5
DEFAULT_ZONES=imgamer.top
MIN_MEM_MB=60     # below: minimal mode (plan 239: Lightlayer TW with 40 MB stayed Phase A only)
APT_MIN_MEM_MB=160 # below: no apt (peaks 74-100 MB RSS), only dpkg -i of pinned + sha256-checked packages
MIN_DISK_MB=80
PROBE_NAME=cloudflare.com
BACKUP_FILES="/etc/resolv.conf /etc/systemd/resolved.conf /etc/network/interfaces /etc/network/interfaces.d/* /etc/resolvconf.conf /etc/resolvconf/resolv.conf.d/head /etc/resolvconf/resolv.conf.d/base /etc/resolvconf/resolv.conf.d/tail /etc/dhcp/dhclient.conf /etc/dhcpcd.conf"

# Known CN resolvers (public + CN-cloud internal). A host that already uses one is treated as a mainland host:
# keeps a CN resolver as the LAST upstream and sends our zones to the fastest of 1.1.1.1 / 1.0.0.1 / 8.8.8.8.
CN_RESOLVERS=" 223.5.5.5 223.6.6.6 119.29.29.29 119.28.28.28 182.254.116.116 182.254.118.118 114.114.114.114 114.114.115.115 180.76.76.76 180.184.1.1 180.184.2.2 1.2.4.8 210.2.4.8 101.226.4.6 218.30.118.6 101.198.198.198 100.100.2.136 100.100.2.138 183.60.83.19 183.60.82.98 100.96.0.2 100.96.0.3 2400:3200::1 2400:3200:baba::1 2402:4e00:: 240c::6666 240c::6644 "

# Pinned packages for low-RAM hosts (no apt): "codename arch file sha256 sha1 pool-path". Official Debian archive files;
# sha256 checked before dpkg -i; sha1 is the snapshot.debian.org/file/<sha1> key for when the mirror dropped the version.
PINNED="
bullseye amd64 libnfnetlink0_1.0.1-3+b1_amd64.deb 6d650aae2fbfcb261557ed9032ba00c6d69ff30eded71050459af0fc0cf50926 b016138323f9d7f2a62af441f0077b07cd9b581c debian/pool/main/libn/libnfnetlink
bullseye amd64 libnetfilter-conntrack3_1.0.8-3_amd64.deb d230aafed9e69a6ee09846b4b9afcecd49e2775651c6d2f22313d21613bc742a c29cee4b7fb1a9d173901a12c65286cc8443f2d2 debian/pool/main/libn/libnetfilter-conntrack
bullseye amd64 dnsmasq-base_2.85-1+deb11u2_amd64.deb 843af1c770d34215d5e8249d113eb924624c3c5ac7f94a734cbe4c30125ad227 d4345b3fa64e7ef42e5aadb462ee261b745c149d debian-security/pool/updates/main/d/dnsmasq
bookworm amd64 libnfnetlink0_1.0.2-2_amd64.deb ae5cbba417ea48f34c1f72c27e8146a81f20614c1296bca2cd7234c8215fddcc c1747ed03e67ca99a7157468ee20d59c38d87cc6 debian/pool/main/libn/libnfnetlink
bookworm amd64 libnetfilter-conntrack3_1.0.9-3_amd64.deb 65f1539238f60fcc85d115acd640474f45c77bfddcf402eb7d75965a783c2bc8 88c8f414f0551f6847521cd35a5b0868bd293cb7 debian/pool/main/libn/libnetfilter-conntrack
bookworm amd64 dnsmasq-base_2.90-4~deb12u2_amd64.deb 77f56034a23b200c9787d3d9b423030a1e95618f476e011d988daacc19919bc1 4ab2dc9804cff2657a5b0649260b01389a4c10ba debian/pool/main/d/dnsmasq
trixie amd64 libnfnetlink0_1.0.2-3_amd64.deb 5e4c3c40a04ffdb4139e4bef1136585e151a8eb8c71efa6c7250b636b3c0d1b1 c6d880a96d0008214396178bc16f9b68e716706d debian/pool/main/libn/libnfnetlink
trixie amd64 libnetfilter-conntrack3_1.1.0-1_amd64.deb ffbac5d06fbe432d5171f2eb5b2504968377f27be133f72c81e71250a9957506 ede83a59aec5cf0e3703abf97acc21252dff3525 debian/pool/main/libn/libnetfilter-conntrack
trixie amd64 dnsmasq-base_2.91-1+deb13u2_amd64.deb fcf9e2d93795a0ca043fd22f0de96748c718b9411a5a48f9c0bb1bdb267d4639 3f60e52edcc53c8a67a83514eb1883def4d1a607 debian/pool/main/d/dnsmasq
"

DRY=0
QUIET=0
CHANGED=0
MODE=""
REASON=""
ZONES_ARG=""
BK=""
TS=$(date -u +%Y%m%dT%H%M%SZ)

say() { [ "$QUIET" = 1 ] || echo "[flvx-dns-cache] $*"; }
warn() { echo "[flvx-dns-cache] WARN $*" >&2; }
is_root() { [ "$(id -u)" = 0 ]; }
has() { command -v "$1" >/dev/null 2>&1; }
pkg_ok() { dpkg-query -W -f='${Status}' "$1" 2>/dev/null | grep -q 'install ok installed'; }
mem_avail() { [ -n "${FLVX_DNS_TEST_MEM_MB:-}" ] && { echo "$FLVX_DNS_TEST_MEM_MB"; return; } # lab only
  awk '/^MemAvailable:/{print int($2/1024); f=1} END{if(!f) print 0}' /proc/meminfo; }
disk_free() { df -Pm "${1:-/}" 2>/dev/null | awk 'NR==2{print $4+0}'; }
is_loop() { case "$1" in 127.*|::1) return 0 ;; esac; return 1; }
is_v6() { case "$1" in *:*) return 0 ;; esac; return 1; }
is_private() { # vendor-internal resolver (RFC1918 / CGNAT / ULA / link-local)
  case "$1" in
    10.*|192.168.*|fd*|fc*|fe80:*) return 0 ;;
    172.*) local b=${1#172.}; b=${b%%.*}; [ "$b" -ge 16 ] 2>/dev/null && [ "$b" -le 31 ] && return 0 ;;
    100.*) local b=${1#100.}; b=${b%%.*}; [ "$b" -ge 64 ] 2>/dev/null && [ "$b" -le 127 ] && return 0 ;;
  esac
  return 1
}
is_cn() { case "$CN_RESOLVERS" in *" $1 "*) return 0 ;; esac; return 1; }
uniq_words() { awk '{for(i=1;i<=NF;i++) if(!s[$i]++) printf "%s ", $i}' <<<"$*" | sed 's/ $//'; }
first_n() { local n=$1; shift; echo "$*" | awk -v n="$n" '{for(i=1;i<=NF&&i<=n;i++) printf "%s%s", (i>1?" ":""), $i}'; }
count() { echo "$*" | wc -w; }

# ---------------------------------------------------------------- facts
ns_list() { awk '/^[[:space:]]*nameserver[[:space:]]/{print $2}' /etc/resolv.conf 2>/dev/null | tr '\n' ' ' | sed 's/ $//'; }
first_ns() { ns_list | awk '{print $1}'; }
opt_words() { awk '/^[[:space:]]*options[[:space:]]/{for(i=2;i<=NF;i++) printf "%s ", $i}' /etc/resolv.conf 2>/dev/null | sed 's/ $//'; }
has_timeout_opt() { # options timeout:N with N <= 2
  local o; for o in $(opt_words); do case "$o" in timeout:1|timeout:2) return 0 ;; esac; done; return 1
}
has_rotate() { local o; for o in $(opt_words); do [ "$o" = rotate ] && return 0; done; return 1; }
merged_opts() { # merged_opts [keep-rotate]: current options minus timeout/attempts(/rotate) plus ours
  local o out=""
  for o in $(opt_words); do
    case "$o" in timeout:*|attempts:*) ;; rotate) [ "${1:-}" = keep-rotate ] && out="$out $o" ;; *) out="$out $o" ;; esac
  done
  uniq_words "$OPTS_WANT $out"
}
own_ns() { # the host's own non-loopback resolvers, in order
  local n out=""
  for n in $(ns_list); do is_loop "$n" || out="$out $n"; done
  if [ -f "$CONF" ] && [ -z "$out" ]; then out=$(conf_upstreams); fi
  uniq_words "$out"
}
lo6() { [ "$(cat /proc/sys/net/ipv6/conf/lo/disable_ipv6 2>/dev/null)" = 0 ] && ip -6 addr show dev lo 2>/dev/null | grep -q '::1/128'; }
svc_active() { systemctl is-active -q "$1" 2>/dev/null; }
svc_enabled() { systemctl is-enabled -q "$1" 2>/dev/null; }
listeners53() { # "addr proc" per line, UDP + TCP
  { ss -Hlnup 'sport = :53' 2>/dev/null; ss -Hlntp 'sport = :53' 2>/dev/null; } |
    awk '{a=$4; p=""; if (match($0, /users:\(\("[^"]+"/)) p=substr($0, RSTART+9, RLENGTH-10); print a, p}' | sort -u
}
resolved_stub_up() { svc_active systemd-resolved && listeners53 | grep -q '^127\.0\.0\.53'; }
port53_conflict() { # prints "addr proc" of a foreign listener that blocks 127.0.0.1:53 / [::1]:53, empty if none
  listeners53 | while read -r a p; do
    case "$p" in dnsmasq) [ -f "$UNIT_FILE" ] && ours_running && continue ;; systemd-resolve*) continue ;; esac
    case "$a" in 127.0.0.1:53|0.0.0.0:53|\*:53|\[::\]:53|\[::1\]:53|\[::ffff:127.0.0.1\]:53|127.0.0.1%*:53) echo "$a ${p:-?}"; return ;; esac
  done
}
owner() { # who writes /etc/resolv.conf
  local t
  if [ -L /etc/resolv.conf ]; then
    t=$(readlink -f /etc/resolv.conf 2>/dev/null || readlink /etc/resolv.conf)
    case "$t" in
      */run/resolvconf/resolv.conf|*/etc/resolvconf/run/resolv.conf) echo resolvconf ;;
      */run/systemd/resolve/stub-resolv.conf) echo resolved-stub ;;
      */run/systemd/resolve/resolv.conf) echo resolved-uplink ;;
      */lib/systemd/resolv.conf) echo resolved-static ;;
      */NetworkManager/*) echo nm ;;
      *) echo "symlink:$t" ;;
    esac
    return
  fi
  [ -e /etc/resolv.conf ] || { echo missing; return; }
  if pkg_ok openresolv || { has resolvconf && resolvconf --version 2>/dev/null | grep -qi openresolv; }; then echo openresolv; return; fi
  if grep -q 'Generated by NetworkManager' /etc/resolv.conf 2>/dev/null && svc_active NetworkManager; then echo nm; return; fi
  echo static
}
ours_running() { # our dnsmasq (comm dnsmasq + our conf on its command line; same rule as the agent)
  local p; for p in $(pgrep -x dnsmasq 2>/dev/null); do grep -qF "$CONF" "/proc/$p/cmdline" 2>/dev/null && return 0; done; return 1
}
ours_present() { [ -f "$UNIT_FILE" ] && [ -f "$CONF" ]; }
conf_upstreams() { grep -E '^server=[0-9a-fA-F:.]+$' "$CONF" 2>/dev/null | cut -d= -f2 | tr '\n' ' ' | sed 's/ $//'; }
conf_zones() { grep -E '^server=/[^/]+/' "$CONF" 2>/dev/null | sed -E 's#^server=/([^/]+)/.*#\1#' | sort -u | tr '\n' ' ' | sed 's/ $//'; }
conf_ourns() { local z; z=$(conf_zones | awk '{print $1}'); [ -n "$z" ] && grep -E "^server=/$z/" "$CONF" | sed -E 's#^server=/[^/]+/##' | tr '\n' ' ' | sed 's/ $//'; }
listen_only_loopback() { # our dnsmasq listens on 127.0.0.1 (and maybe ::1) only
  local a bad=0 n=0
  for a in $(listeners53 | awk '$2=="dnsmasq"{print $1}'); do
    n=$((n + 1)); case "$a" in 127.0.0.1:53|\[::1\]:53) ;; *) bad=1 ;; esac
  done
  [ "$n" -gt 0 ] && [ "$bad" = 0 ]
}

# ---------------------------------------------------------------- DNS probe (bash /dev/udp, no dig needed)
# dns_q SERVER NAME [TIMEOUT_S] -> prints "RCODE MS" (RCODE: 0 NOERROR, 3 NXDOMAIN, 2 SERVFAIL, 5 REFUSED, T timeout, E error)
dns_q() {
  local s=$1 n=$2 t=${3:-2} id hi lo q l fd r t0 t1 rc
  id=$(( (RANDOM * 2 + RANDOM) % 65536 )); hi=$((id / 256)); lo=$((id % 256))
  q=$(printf '\\x%02x\\x%02x\\x01\\x00\\x00\\x01\\x00\\x00\\x00\\x00\\x00\\x00' "$hi" "$lo")
  for l in ${n//./ }; do q+=$(printf '\\x%02x' "${#l}")$l; done
  q+='\x00\x00\x01\x00\x01'
  t0=$(date +%s%N)
  if ! exec {fd}<>"/dev/udp/$s/53"; then echo "E 0"; return 1; fi 2>/dev/null
  # one write() = one datagram: printf may flush at a 0x0a byte (10-char label), so let dd collect the whole query
  printf "$q" | dd bs=512 count=1 iflag=fullblock 2>/dev/null >&"$fd"
  r=$(timeout "$t" dd bs=1500 count=1 <&"$fd" 2>/dev/null | od -An -tx1 -N4 | tr -d ' \n')
  exec {fd}>&-
  t1=$(date +%s%N)
  if [ ${#r} -ne 8 ]; then echo "T $(( (t1 - t0) / 1000000 ))"; return 1; fi
  if [ "${r:0:4}" != "$(printf '%02x%02x' "$hi" "$lo")" ]; then echo "E $(( (t1 - t0) / 1000000 ))"; return 1; fi
  rc=$(( 16#${r:6:2} % 16 ))
  echo "$rc $(( (t1 - t0) / 1000000 ))"
  [ "$rc" = 0 ] || [ "$rc" = 3 ]
}
probe_ok() { dns_q "$1" "${2:-$PROBE_NAME}" "${3:-2}" >/dev/null; }
alive() { # alive LIST -> the members that answer a query (sequential, 1.5 s, one retry: a lost probe must not drop a live resolver)
  local s out=""
  for s in "$@"; do { probe_ok "$s" "$PROBE_NAME" 1.5 || probe_ok "$s" "$PROBE_NAME" 1.5; } && out="$out $s"; done
  uniq_words "$out"
}
fastest() { # fastest N LIST -> N fastest answering servers
  local n=$1 s r; shift
  for s in "$@"; do r=$(dns_q "$s" "$PROBE_NAME" 1.5) && echo "${r#* } $s"; done | sort -n | awk -v n="$n" 'NR<=n{printf "%s%s", (NR>1?" ":""), $2}'
}

# ---------------------------------------------------------------- zones
norm_zone() { # host or zone -> registrable domain (last 2 labels; 3 for co.uk-style); empty for IPs / junk
  local h=${1,,}; h=${h%.}; h=${h#*://}; h=${h%%/*}
  case "$h" in \[*) return ;; esac
  h=${h%:*}
  [[ "$h" =~ ^[a-z0-9.-]+$ ]] || return
  [[ "$h" =~ ^[0-9.]+$ ]] && return
  [[ "$h" == *.* ]] || return
  local IFS=. a; read -r -a a <<<"$h"; local n=${#a[@]}
  if [ "$n" -ge 3 ] && [ ${#a[n-1]} -eq 2 ] && [[ "${a[n-2]}" =~ ^(com|net|org|gov|edu|co|ac|or|ne|go)$ ]]; then
    echo "${a[n-3]}.${a[n-2]}.${a[n-1]}"
  else echo "${a[n-2]}.${a[n-1]}"; fi
}
want_zones() { # union of existing conf zones + --zones (normalised); default imgamer.top
  local z out
  out=$(conf_zones)
  for z in $ZONES_ARG; do out="$out $(norm_zone "$z")"; done
  out=$(uniq_words "$out")
  [ -n "$out" ] || out=$DEFAULT_ZONES
  echo "$out" | tr ' ' '\n' | sort -u | head -12 | tr '\n' ' ' | sed 's/ $//'
}

# ---------------------------------------------------------------- backup / rollback (plan-239 layout)
pick_bk() {
  [ -n "$BK" ] && return
  local d
  for d in $(ls -d /root/dns-backup-* 2>/dev/null | sort); do [ -f "$d/orig.tgz" ] && BK=$d; done
  [ -n "$BK" ] || BK=/root/dns-backup-$(date +%Y%m%d)
}
existing_files() { local f; for f in $BACKUP_FILES; do { [ -e "$f" ] || [ -L "$f" ]; } && echo "$f"; done; }
created() { # created PHASE PATH
  grep -qxF "$1 $2" "$BK/created.list" 2>/dev/null || echo "$1 $2" >> "$BK/created.list"
}
write_rollback() {
  [ -f "$BK/rollback.sh" ] && grep -q 'plan 239' "$BK/rollback.sh" && return # plan 239 already wrote one for this layout
  cat > "$BK/rollback.sh" <<EOF
#!/bin/sh
# FLVX dns cache rollback (flvx-dns-cache, plan 240)
#   rollback.sh      = back to the DNS config from before the first change (orig.tgz)
#   rollback.sh b    = undo only the local cache -> resolver order + timeout stay (pre-b.tgz)
set -u
BK=$BK
case "\${1:-all}" in all) T=\$BK/orig.tgz; L="A B";; b) T=\$BK/pre-b.tgz; L="B";; *) echo "usage: \$0 [all|b]"; exit 2;; esac
[ -f "\$T" ] || { echo "missing \$T"; exit 1; }
lsattr /etc/resolv.conf 2>/dev/null | cut -d' ' -f1 | grep -q i && chattr -i /etc/resolv.conf
# 1) resolv.conf + sources back FIRST (the cache still runs, so lookups keep working)
tar -tzf "\$T" -P | while read -r f; do if [ -L "\$f" ] || [ -f "\$f" ]; then rm -f -- "\$f"; fi; done
tar -xzpf "\$T" -P
# 2) stop + remove what we created
if [ -f /etc/systemd/system/flvx-dnscache.service ] && echo "\$L" | grep -q B; then systemctl disable --now flvx-dnscache.service; fi
for ph in \$L; do grep "^\$ph " "\$BK/created.list" 2>/dev/null | cut -d' ' -f2- | while read -r f; do rm -rf -- "\$f"; done; done
systemctl daemon-reload
if [ -L /etc/resolv.conf ] && readlink /etc/resolv.conf | grep -q resolvconf; then resolvconf -u; fi
if dpkg-query -W -f='\${Status}' openresolv 2>/dev/null | grep -q 'ok installed'; then resolvconf -u; fi
if [ -f "\$BK/resolved-changed" ] && systemctl is-active -q systemd-resolved; then systemctl restart systemd-resolved; fi
rm -f /etc/flvx-dnscache/state
grep -q -- '----i' "\$BK/orig.meta" 2>/dev/null && [ "\$L" = "A B" ] && chattr +i /etc/resolv.conf 2>/dev/null
echo "--- /etc/resolv.conf now:"; ls -la /etc/resolv.conf; grep -v '^#' /etc/resolv.conf
echo "(package dnsmasq-base stays installed but unused; remove with: apt-get purge dnsmasq-base)"
EOF
  chmod 700 "$BK/rollback.sh"
}
backup() { # before the first change of this run
  [ "$DRY" = 1 ] && return
  [ -n "${BACKED:-}" ] && return
  pick_bk
  mkdir -p "$BK" && chmod 700 "$BK"
  if [ ! -f "$BK/orig.tgz" ]; then
    tar -czpf "$BK/orig.tgz" -P $(existing_files) 2>/dev/null || { warn "backup failed"; return 1; }
    { ls -la /etc/resolv.conf; readlink -f /etc/resolv.conf; lsattr /etc/resolv.conf 2>/dev/null; echo ---; cat /etc/resolv.conf; } > "$BK/orig.meta" 2>&1
    say "backup $BK/orig.tgz"
  fi
  tar -czpf "$BK/snap-$TS-ensure.tgz" -P $(existing_files) 2>/dev/null
  ls -t "$BK"/snap-*-ensure.tgz 2>/dev/null | tail -n +6 | xargs -r rm -f # keep the 5 newest of our snapshots
  write_rollback
  BACKED=1
}
pre_b_backup() { [ "$DRY" = 1 ] && return; backup; [ -f "$BK/pre-b.tgz" ] || tar -czpf "$BK/pre-b.tgz" -P $(existing_files) 2>/dev/null; }

# ---------------------------------------------------------------- resolv.conf writers
put_resolv() { # put_resolv "<comment>" "ns..." "<options>": atomic static /etc/resolv.conf (search/domain/sortlist kept)
  local hdr=$1 nss=$2 op=$3 keep tmp immut=0 n
  if [ "$DRY" = 1 ]; then say "dry-run: resolv.conf -> nameserver $nss; options $op"; return 0; fi
  keep=$(grep -E '^(search|domain|sortlist)[[:space:]]' /etc/resolv.conf 2>/dev/null)
  tmp=/etc/.resolv.conf.flvx-$$
  { echo "# $hdr"; echo "# FLVX flvx-dns-cache (plan 240). Rollback: $BK/rollback.sh"
    for n in $nss; do echo "nameserver $n"; done
    [ -n "$op" ] && echo "options $op"
    [ -z "$keep" ] || echo "$keep"; } > "$tmp" || { rm -f "$tmp"; return 1; }
  chmod 644 "$tmp"
  if lsattr /etc/resolv.conf 2>/dev/null | cut -d' ' -f1 | grep -q i; then chattr -i /etc/resolv.conf; immut=1; fi
  if ! mv -f "$tmp" /etc/resolv.conf 2>/dev/null; then
    cat "$tmp" > /etc/resolv.conf || { rm -f "$tmp"; return 1; } # bind-mounted resolv.conf (containers)
    rm -f "$tmp"
  fi
  [ "$immut" = 1 ] && chattr +i /etc/resolv.conf
  CHANGED=1
  say "resolv.conf: $(grep -v '^#' /etc/resolv.conf | tr '\n' ';')"
}
set_kv() { # set_kv FILE KEY VALUE (shell-style KEY=VALUE line, openresolv)
  local f=$1 k=$2 v=$3
  grep -q "^$k=" "$f" 2>/dev/null && grep -qxF "$k=\"$v\"" "$f" && return 0
  if [ "$DRY" = 1 ]; then say "dry-run: $f: $k=\"$v\""; return 0; fi
  if grep -q "^$k=" "$f" 2>/dev/null; then sed -i "s|^$k=.*|$k=\"$v\"|" "$f"; else echo "$k=\"$v\"  # FLVX flvx-dns-cache" >> "$f"; fi
  CHANGED=1
}
hooks_static() { # keep a hand-written resolv.conf across DHCP renews / LXC restarts
  if pgrep -x dhclient >/dev/null 2>&1 && [ -d /etc/dhcp/dhclient-enter-hooks.d ] && [ ! -f "$DHCLIENT_HOOK" ]; then
    if [ "$DRY" = 1 ]; then say "dry-run: dhclient hook"; else
      printf '%s\n' '# FLVX flvx-dns-cache: keep /etc/resolv.conf (local DNS cache), ignore DHCP DNS on renew.' 'make_resolv_conf() { :; }' > "$DHCLIENT_HOOK"
      created A "$DHCLIENT_HOOK"; CHANGED=1; say "dhclient hook $DHCLIENT_HOOK"
    fi
  fi
  if pgrep -x dhcpcd >/dev/null 2>&1 && [ -f /etc/dhcpcd.conf ] && ! grep -qE '^[[:space:]]*nohook[[:space:]].*resolv\.conf' /etc/dhcpcd.conf; then
    if [ "$DRY" = 1 ]; then say "dry-run: dhcpcd nohook resolv.conf"; else
      printf '%s\n' '# FLVX flvx-dns-cache: keep /etc/resolv.conf (local DNS cache)' 'nohook resolv.conf' >> /etc/dhcpcd.conf; CHANGED=1; say "dhcpcd: nohook resolv.conf"
    fi
  fi
  if [ "$(systemd-detect-virt 2>/dev/null)" = lxc ] && [ ! -e /etc/.pve-ignore.resolv.conf ]; then
    if [ "$DRY" = 1 ]; then say "dry-run: /etc/.pve-ignore.resolv.conf"; else
      : > /etc/.pve-ignore.resolv.conf; created A /etc/.pve-ignore.resolv.conf; CHANGED=1; say "pve-ignore.resolv.conf"
    fi
  fi
}
hooks_ok() {
  if pgrep -x dhclient >/dev/null 2>&1 && [ -d /etc/dhcp/dhclient-enter-hooks.d ] && [ ! -f "$DHCLIENT_HOOK" ]; then return 1; fi
  if pgrep -x dhcpcd >/dev/null 2>&1 && [ -f /etc/dhcpcd.conf ] && ! grep -qE '^[[:space:]]*nohook[[:space:]].*resolv\.conf' /etc/dhcpcd.conf; then return 1; fi
  if [ "$(systemd-detect-virt 2>/dev/null)" = lxc ] && [ ! -e /etc/.pve-ignore.resolv.conf ]; then return 1; fi
  return 0
}

# point_local OWNER LOCAL FB: resolv.conf -> LOCAL (127.0.0.1 or 127.0.0.53) first, FB after it, timeout:1, no rotate
point_local() {
  local own=$1 loc=$2 fb=$3 op
  case "$own" in
    static|resolved-uplink|resolved-static|missing|symlink:/run/systemd/resolve/*)
      op=$(merged_opts); [ "$loc" = 127.0.0.53 ] && op=$(uniq_words "$op edns0 trust-ad")
      if [ "$(ns_list)" = "$(uniq_words "$loc $fb")" ] && [ ! -L /etc/resolv.conf ] && has_timeout_opt && ! has_rotate; then :; else
        pre_b_backup; put_resolv "local DNS cache first ($loc); public fallback if it is down" "$loc $fb" "$op" || return 1
      fi
      hooks_static ;;
    openresolv)
      [ "$DRY" = 1 ] || pre_b_backup
      [ -f /etc/resolvconf.conf ] || { [ "$DRY" = 1 ] || : > /etc/resolvconf.conf; }
      set_kv /etc/resolvconf.conf name_servers "$loc"
      set_kv /etc/resolvconf.conf resolv_conf_local_only NO
      set_kv /etc/resolvconf.conf resolv_conf_options "$OPTS_WANT"
      [ "$DRY" = 1 ] && return 0
      resolvconf -u 2>/dev/null
      if [ "$(count "$(ns_list)")" -lt 2 ]; then set_kv /etc/resolvconf.conf name_servers_append "$fb"; resolvconf -u 2>/dev/null; fi
      say "resolv.conf: $(grep -v '^#' /etc/resolv.conf | tr '\n' ';')" ;;
    resolvconf)
      local h=/etc/resolvconf/resolv.conf.d/head t=/etc/resolvconf/resolv.conf.d/tail b=/etc/resolvconf/resolv.conf.d/base
      if [ "$DRY" = 1 ]; then say "dry-run: resolvconf head nameserver $loc, tail options $OPTS_WANT"; return 0; fi
      pre_b_backup
      [ -f "$h" ] || { : > "$h"; created B "$h"; }
      [ -f "$t" ] || { : > "$t"; created B "$t"; }
      grep -qx "nameserver $loc" "$h" || { echo "nameserver $loc" >> "$h"; CHANGED=1; }
      if grep -q '^options' "$t"; then grep -qx "options $OPTS_WANT" "$t" || { sed -i "s/^options.*/options $OPTS_WANT/" "$t"; CHANGED=1; }
      else echo "options $OPTS_WANT" >> "$t"; CHANGED=1; fi
      resolvconf -u 2>/dev/null
      if [ "$(count "$(ns_list)")" -lt 2 ]; then
        [ -f "$b" ] || { : > "$b"; created B "$b"; }
        for n in $fb; do grep -qx "nameserver $n" "$b" || echo "nameserver $n" >> "$b"; done; resolvconf -u 2>/dev/null; CHANGED=1
      fi
      say "resolv.conf: $(grep -v '^#' /etc/resolv.conf | tr '\n' ';')" ;;
    *) REASON="resolv.conf 由 ${own#symlink:} 管理"; return 1 ;;
  esac
  [ "$DRY" = 1 ] && return 0
  [ "$(first_ns)" = "$loc" ] || { REASON="resolv.conf 未指向 $loc"; return 1; }
}
points_local_ok() { # resolv.conf already right for LOCAL (incl. >= 2 nameservers, timeout, no rotate, hooks)
  local loc=$1
  [ "$(first_ns)" = "$loc" ] || return 1
  [ "$(count "$(ns_list)")" -ge 2 ] || return 1
  has_timeout_opt && ! has_rotate || return 1
  case "$(owner)" in static|resolved-uplink|resolved-static) hooks_ok || return 1 ;; esac
  return 0
}

# ---------------------------------------------------------------- minimal mode
to_minimal() { # >= 2 resolvers (host's own working ones + public) + timeout:1 attempts:2; cache not used
  local own fb o
  own=$(owner)
  if ! ours_present || [ "$DRY" = 1 ]; then :; else systemctl disable --now "$UNIT" >/dev/null 2>&1; fi
  if ! is_loop "$(first_ns)" && [ "$(count "$(ns_list)")" -ge 2 ] && has_timeout_opt; then
    case "$own" in static|resolved-uplink|resolved-static) hooks_static ;; esac
    return 0
  fi
  o=$(alive $(own_ns))
  if [ "$(count "$o")" -lt 2 ]; then o=$(uniq_words "$o $(alive $PUBLIC4)"); fi
  [ "$(count "$o")" -ge 1 ] || o=$(uniq_words "$o $(alive $PUBLIC6)")
  [ -n "$o" ] || o=$(own_ns)
  fb=$(first_n 3 "$o")
  case "$own" in
    static|resolved-uplink|resolved-static)
      backup; put_resolv "resolvers in order of reliability; 1 s timeout instead of 5 s (no local cache: $REASON)" "$fb" "$(merged_opts keep-rotate)" || return 1
      hooks_static ;;
    openresolv)
      backup
      [ "$DRY" = 1 ] && return 0
      grep -q '^name_servers="127\.0\.0\.1"' /etc/resolvconf.conf 2>/dev/null && sed -i '/^name_servers="127\.0\.0\.1"/d' /etc/resolvconf.conf
      set_kv /etc/resolvconf.conf resolv_conf_options "$OPTS_WANT"; resolvconf -u 2>/dev/null
      [ "$(count "$(ns_list)")" -ge 2 ] || { set_kv /etc/resolvconf.conf name_servers_append "$fb"; resolvconf -u 2>/dev/null; } ;;
    resolvconf)
      backup
      [ "$DRY" = 1 ] && return 0
      sed -i '/^nameserver 127\.0\.0\.1$/d' /etc/resolvconf/resolv.conf.d/head 2>/dev/null
      local t=/etc/resolvconf/resolv.conf.d/tail; [ -f "$t" ] || : > "$t"
      if grep -q '^options' "$t"; then sed -i "s/^options.*/options $OPTS_WANT/" "$t"; else echo "options $OPTS_WANT" >> "$t"; fi
      resolvconf -u 2>/dev/null; CHANGED=1 ;;
    *) : ;; # NetworkManager / foreign symlink: leave it, only report
  esac
}

# ---------------------------------------------------------------- package
pinned_rows() { local cn=$1 ar=$2; echo "$PINNED" | awk -v c="$cn" -v a="$ar" '$1==c && $2==a'; }
deps_ok_for() { # deps_ok_for DEB BATCH_PKGS: every Depends (any alternative) installed or in the batch
  local d alt okd
  while IFS= read -r d; do
    okd=0
    for alt in $(echo "$d" | tr '|' '\n' | sed -E 's/\(.*\)//; s/:any//' | tr -d ' '); do
      { pkg_ok "$alt" || case " $2 " in *" $alt "*) true ;; *) false ;; esac; } && { okd=1; break; }
    done
    [ "$okd" = 1 ] || { echo "$d"; return 1; }
  done < <(dpkg-deb -f "$1" Depends 2>/dev/null | tr ',' '\n' | sed 's/^ *//')
}
fetch_verified() { # fetch_verified OUT SHA256 URL... -> first URL whose content matches the sha256
  local out=$1 sha=$2 u; shift 2
  for u in "$@"; do
    has curl && curl -fsSL --connect-timeout 8 -m 60 -o "$out.part" "$u" 2>/dev/null || { has wget && wget -q -T 30 -O "$out.part" "$u" 2>/dev/null; } || continue
    if [ "$(sha256sum "$out.part" | cut -d' ' -f1)" = "$sha" ]; then mv -f "$out.part" "$out"; return 0; fi
    warn "sha256 mismatch from $u"
  done
  rm -f "$out.part"; return 1
}
install_pinned() { # low-RAM path: no apt; dpkg -i of pinned, sha256-checked official Debian packages
  local cn ar rows D batch="" files="" f sha sha1 path pk
  . /etc/os-release 2>/dev/null; cn=${VERSION_CODENAME:-}; ar=$(dpkg --print-architecture 2>/dev/null)
  rows=$(pinned_rows "$cn" "$ar")
  [ -n "$rows" ] || { REASON="可用内存 $(mem_avail) MB，不够运行 apt，且没有 $cn/$ar 的预置安装包"; return 1; }
  D=/var/cache/apt/archives; mkdir -p "$D"
  while read -r _ _ f sha sha1 path; do
    [ -n "$f" ] || continue
    pk=${f%%_*}
    pkg_ok "$pk" && continue
    if [ ! -f "$D/$f" ] || [ "$(sha256sum "$D/$f" | cut -d' ' -f1)" != "$sha" ]; then
      fetch_verified "$D/$f" "$sha" "http://deb.debian.org/$path/$f" "http://security.debian.org/$path/$f" \
        "http://archive.debian.org/$path/$f" "https://snapshot.debian.org/file/$sha1" || { REASON="下载 $f 失败（或校验不符）"; return 1; }
    fi
    batch="$batch $pk"; files="$files $D/$f"
  done <<<"$rows"
  for f in $files; do deps_ok_for "$f" "$batch" >/dev/null || { REASON="$(basename "$f") 缺少依赖 $(deps_ok_for "$f" "$batch")"; return 1; }; done
  say "dpkg -i (pinned, sha256 ok):$batch"
  if ! DEBIAN_FRONTEND=noninteractive timeout 180 dpkg -i $files > "$BK/dpkg-$TS.log" 2>&1; then
    tail -5 "$BK/dpkg-$TS.log" >&2
    for pk in $batch; do dpkg-query -W -f='${Status}' "$pk" 2>/dev/null | grep -q 'ok installed' || dpkg -r "$pk" >/dev/null 2>&1; done
    REASON="dpkg 安装 dnsmasq-base 失败"; return 1
  fi
  echo "B-pkg dnsmasq-base$batch" >> "$BK/created.list"
}
apt_fetch_fallback() { # mirror 404 / 502: same versions apt wants, from official archives, sha256 from the signed apt index
  local sim=$1 p v fn sha bases D=/var/cache/apt/archives
  case "$(. /etc/os-release; echo "$ID")" in
    ubuntu) bases="http://archive.ubuntu.com/ubuntu http://security.ubuntu.com/ubuntu http://ports.ubuntu.com/ubuntu-ports http://old-releases.ubuntu.com/ubuntu" ;;
    *) bases="http://deb.debian.org/debian http://deb.debian.org/debian-security http://security.debian.org/debian-security http://archive.debian.org/debian http://archive.debian.org/debian-security" ;;
  esac
  while read -r p v; do
    fn=$(apt-cache show "$p=$v" 2>/dev/null | awk '/^Filename:/{print $2; exit}')
    sha=$(apt-cache show "$p=$v" 2>/dev/null | awk '/^SHA256:/{print $2; exit}')
    [ -n "$fn" ] && [ -n "$sha" ] || return 1
    [ -f "$D/$(basename "$fn")" ] && [ "$(sha256sum "$D/$(basename "$fn")" | cut -d' ' -f1)" = "$sha" ] && continue
    local urls="" b; for b in $bases; do urls="$urls $b/$fn"; done
    fetch_verified "$D/$(basename "$fn")" "$sha" $urls || return 1
    say "fetched $(basename "$fn") (sha256 matches apt index)"
  done < <(echo "$sim" | awk '/^Inst /{gsub(/[()]/,""); print $2, $3}')
  DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1 NEEDRESTART_MODE=l timeout 180 \
    apt-get install -y -q --no-download --no-install-recommends -o DPkg::Lock::Timeout=60 -o Dpkg::Options::=--force-confold dnsmasq-base >> "$BK/apt-$TS.log" 2>&1
}
install_apt() {
  local sim up
  sim=$(timeout 60 apt-get -s install --no-install-recommends dnsmasq-base 2>&1)
  if ! echo "$sim" | grep -q '^Inst dnsmasq-base' && [ "$(mem_avail)" -ge 200 ] && [ -z "$(ls /var/lib/apt/lists/*Packages* 2>/dev/null | head -1)" ]; then
    say "apt lists empty -> apt-get update"; timeout 180 apt-get update -q >/dev/null 2>&1
    sim=$(timeout 60 apt-get -s install --no-install-recommends dnsmasq-base 2>&1)
  fi
  echo "$sim" | grep -q '^Inst dnsmasq-base' || { REASON="apt 找不到 dnsmasq-base"; return 1; }
  up=$(echo "$sim" | grep -E '^Inst [^ ]+ \[' | cut -d' ' -f2 | tr '\n' ' ')
  [ -z "$up" ] || { REASON="安装会升级已有软件包（$up），已拒绝"; return 1; }
  say "apt-get install dnsmasq-base ($(echo "$sim" | awk '/^Inst /{printf "%s ", $2}'))"
  if DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1 NEEDRESTART_MODE=l timeout 150 \
       apt-get install -y -q --no-install-recommends -o DPkg::Lock::Timeout=60 -o Dpkg::Options::=--force-confold dnsmasq-base > "$BK/apt-$TS.log" 2>&1; then
    echo "B-pkg dnsmasq-base $(grep -E '^Setting up' "$BK/apt-$TS.log" | cut -d' ' -f3 | tr '\n' ' ')" >> "$BK/created.list"; return 0
  fi
  if grep -qE '404|502|503|Unable to fetch|Failed to fetch|Temporary failure' "$BK/apt-$TS.log"; then
    say "apt mirror could not deliver the package -> official archive fallback"
    if apt_fetch_fallback "$sim"; then echo "B-pkg dnsmasq-base (archive fallback)" >> "$BK/created.list"; return 0; fi
  fi
  tail -3 "$BK/apt-$TS.log" >&2
  REASON="apt 安装 dnsmasq-base 失败"; return 1
}
install_dnsmasq() {
  [ -x /usr/sbin/dnsmasq ] && return 0
  has dpkg && has apt-get || { REASON="不是 Debian/Ubuntu，无法自动安装 dnsmasq"; return 1; }
  local ma dk; ma=$(mem_avail); dk=$(disk_free /var)
  [ "${dk:-0}" -ge "$MIN_DISK_MB" ] || { REASON="磁盘剩余 ${dk} MB"; return 1; }
  if [ "$DRY" = 1 ]; then say "dry-run: would install dnsmasq-base ($([ "$ma" -ge "$APT_MIN_MEM_MB" ] && echo apt || echo pinned dpkg), MemAvailable $ma MB)"; return 0; fi
  pre_b_backup
  if [ "$ma" -ge "$APT_MIN_MEM_MB" ]; then install_apt && return 0; local r=$REASON; install_pinned && return 0; REASON=$r; return 1; fi
  install_pinned
}

# ---------------------------------------------------------------- dnsmasq cache
pick_upstreams() { # sets UP, OURNS for a fresh setup (plan-239 decisions)
  local own a o vendor=0 cn=0 s pub=""
  own=$(own_ns)
  for s in $own; do is_private "$s" && vendor=1; is_cn "$s" && cn=1; done
  o=$(alive $own)
  if [ "$vendor" = 1 ]; then # vendor-internal DNS (may do split/unlock): keep it, don't mix public servers in
    UP=$(first_n 3 "$o")
    if [ "$(count "$UP")" -lt 2 ]; then
      if [ "$cn" = 1 ]; then pub=$(alive $CN_PUBLIC); else pub=$(first_n 1 "$(alive $PUBLIC4)"); fi
      UP=$(uniq_words "$UP $pub")
    fi
    if [ "$cn" = 1 ] && [ "$(count "$UP")" -lt 3 ] && ! echo " $UP " | grep -q " $CN_PUBLIC "; then
      UP=$(uniq_words "$UP $(alive $CN_PUBLIC)")
    fi
  elif [ "$cn" = 1 ]; then # mainland: public first, one CN resolver LAST
    a=$(alive $PUBLIC4); local c=""
    for s in $o; do is_cn "$s" && { c=$s; break; }; done
    [ -n "$c" ] || c=$(alive $CN_PUBLIC)
    UP=$(uniq_words "$(first_n 2 "$a") $c")
  else
    local ownpub=""; for s in $o; do is_cn "$s" || ownpub="$ownpub $s"; done
    a=$(alive $PUBLIC4)
    UP=$(first_n 3 "$(uniq_words "$(first_n 2 "$ownpub") $a")")
  fi
  [ "$(count "$UP")" -ge 1 ] || UP=$(first_n 2 "$(alive $PUBLIC6)")
  [ "$(count "$UP")" -ge 1 ] || UP=$(first_n 2 "$own")
  if [ "$cn" = 1 ]; then OURNS=$(fastest 2 1.1.1.1 1.0.0.1 8.8.8.8); else OURNS=$(alive $OURNS_DEFAULT); fi
  [ -n "$OURNS" ] || OURNS=$(alive 2606:4700:4700::1111 2606:4700:4700::1001)
  [ -n "$OURNS" ] || OURNS=$OURNS_DEFAULT
}
render_conf() { # render_conf UP ZONES OURNS -> stdout
  local listen=127.0.0.1 s z u=dnsmasq
  lo6 && listen=127.0.0.1,::1
  id dnsmasq >/dev/null 2>&1 || u=nobody
  echo "# FLVX local DNS cache (flvx-dns-cache v$SCRIPT_VERSION, plan 240 = plan-239 recipe): for THIS host only."
  echo "# Loopback only, never reachable from outside. No DHCP / TFTP. No query log."
  echo "# Upstreams are asked in parallel (all-servers), first answer wins. Our own names ($2) go to $3."
  echo "listen-address=$listen"; echo "bind-interfaces"; echo "port=53"
  echo "no-dhcp-interface="; echo "no-resolv"; echo "no-poll"; echo "all-servers"
  for s in $1; do echo "server=$s"; done
  for z in $2; do for s in $3; do echo "server=/$z/$s"; done; done
  echo "cache-size=1000"; echo "max-cache-ttl=300"; echo "neg-ttl=30"
  echo "domain-needed"; echo "bogus-priv"
  echo "user=$u"; echo "group=nogroup"; echo "pid-file="
}
render_unit() {
  cat <<'EOF'
[Unit]
Description=FLVX local DNS cache (dnsmasq, 127.0.0.1/::1 only; flvx-dns-cache)
After=network.target
Before=nss-lookup.target
Wants=nss-lookup.target
StartLimitIntervalSec=600
StartLimitBurst=5

[Service]
Type=simple
ExecStartPre=/usr/sbin/dnsmasq --test --conf-file=/etc/flvx-dnscache/dnsmasq.conf
ExecStart=/usr/sbin/dnsmasq --keep-in-foreground --conf-file=/etc/flvx-dnscache/dnsmasq.conf
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=5
MemoryMax=32M
CPUQuota=20%
TasksMax=8
LimitNOFILE=1024
PrivateTmp=yes
ProtectHome=yes
ProtectSystem=full

[Install]
WantedBy=multi-user.target
EOF
}
cache_answers() { # the cache (127.0.0.1) answers a normal name and one of our zones
  local z; z=$(conf_zones | awk '{print $1}')
  probe_ok 127.0.0.1 "$PROBE_NAME" 3 || probe_ok 127.0.0.1 "$PROBE_NAME" 3 || return 1
  [ -z "$z" ] || probe_ok 127.0.0.1 "$z" 3 || probe_ok 127.0.0.1 "$z" 3
}
start_cache() { # (re)start the unit and verify listening + answers; on failure stop it
  systemctl daemon-reload
  if svc_active "$UNIT"; then systemctl restart "$UNIT"; else systemctl reset-failed "$UNIT" >/dev/null 2>&1; systemctl enable --now "$UNIT" >/dev/null 2>&1; fi
  local i; for i in 1 2 3 4 5; do listen_only_loopback && break; sleep 1; done
  if ! svc_active "$UNIT" || ! listen_only_loopback; then
    local why; why=$(journalctl -u "$UNIT" -n 3 --no-pager -o cat 2>/dev/null | tail -1)
    systemctl disable --now "$UNIT" >/dev/null 2>&1
    REASON="本机缓存启动失败${why:+：$why}"; return 1
  fi
  cache_answers || { systemctl disable --now "$UNIT" >/dev/null 2>&1; REASON="本机缓存自检失败（上游都不通）"; return 1; }
}
ensure_dnsmasq() { # install / configure / repair our cache; returns 1 (with REASON) when the host should go minimal
  local zones up ourns new conf_changed=0 unit_changed=0
  zones=$(want_zones)
  if ours_present && [ -n "$(conf_upstreams)" ] && grep -q '^listen-address=127\.0\.0\.1' "$CONF" && grep -q '^bind-interfaces' "$CONF"; then
    up=$(conf_upstreams); ourns=$(conf_ourns); [ -n "$ourns" ] || ourns=$OURNS_DEFAULT
    if [ "$(conf_zones)" != "$zones" ]; then conf_changed=1; fi
  else
    pick_upstreams; up=$UP; ourns=$OURNS; conf_changed=1
  fi
  install_dnsmasq || return 1
  [ "$DRY" = 1 ] && { say "dry-run: cache upstreams [$up] zones [$zones] -> [$ourns]$([ $conf_changed = 1 ] && echo ' (config would change)')"; return 0; }
  [ -x /usr/sbin/dnsmasq ] || { REASON="没有 dnsmasq 程序"; return 1; }
  if [ "$conf_changed" = 1 ]; then
    pre_b_backup
    [ -d "$DIR" ] || { mkdir -p "$DIR"; created B "$DIR"; }
    render_conf "$up" "$zones" "$ourns" > "$CONF.new"
    /usr/sbin/dnsmasq --test --conf-file="$CONF.new" >/dev/null 2>&1 || { rm -f "$CONF.new"; REASON="dnsmasq 配置自检失败"; return 1; }
    mv -f "$CONF.new" "$CONF"; CHANGED=1; say "cache config: upstreams [$up], zones [$zones] -> [$ourns]"
  fi
  if [ ! -f "$UNIT_FILE" ]; then
    pre_b_backup; render_unit > "$UNIT_FILE"; created B "$UNIT_FILE"; unit_changed=1; CHANGED=1
  fi
  if [ "$conf_changed" = 1 ] || [ "$unit_changed" = 1 ] || ! svc_active "$UNIT" || ! svc_enabled "$UNIT" || ! listen_only_loopback; then
    start_cache || return 1
    say "cache running: $(listeners53 | awk '$2=="dnsmasq"{printf "%s ", $1}')"
  elif ! cache_answers; then
    say "cache not answering -> re-pick upstreams"
    pick_upstreams; render_conf "$UP" "$zones" "$OURNS" > "$CONF.new" && mv -f "$CONF.new" "$CONF"; CHANGED=1
    start_cache || return 1
  fi
  if ! points_local_ok 127.0.0.1; then
    local fb; fb=$(first_n 2 "$(alive $(own_ns))")
    [ "$(count "$fb")" -ge 1 ] || fb=$(first_n 2 "$up")
    [ "$(count "$fb")" -ge 2 ] || fb=$(first_n 2 "$(uniq_words "$fb $up $(alive $PUBLIC4)")")
    point_local "$(owner)" 127.0.0.1 "$fb" || return 1
  fi
  probe_ok 127.0.0.1 "$PROBE_NAME" 3 || { REASON="切换后本机缓存不应答"; return 1; }
}

# ---------------------------------------------------------------- resolved mode
resolved_servers() { resolvectl dns 2>/dev/null | sed 's/^[^:]*://' | tr ' ' '\n' | grep -E '^[0-9a-fA-F:.]+(#.*)?$' | sed 's/#.*//' | sort -u | tr '\n' ' ' | sed 's/ $//'; }
ensure_resolved() {
  local srv add fb
  srv=$(resolved_servers)
  if [ "$(count "$srv")" -lt 2 ]; then
    add=""; for s in $(alive $PUBLIC4); do case " $srv " in *" $s "*) ;; *) add="$add $s" ;; esac; done
    add=$(first_n $((2 - $(count "$srv"))) "$add")
    if [ -n "$add" ]; then
      if [ "$DRY" = 1 ]; then say "dry-run: resolved global DNS +$add"; else
        backup; mkdir -p "$(dirname "$RESOLVED_DROPIN")"
        printf '%s\n' '# FLVX flvx-dns-cache: second upstream so one dead resolver never stalls lookups' '[Resolve]' "DNS=$(uniq_words "$add")" > "$RESOLVED_DROPIN"
        created A "$RESOLVED_DROPIN"; : > "$BK/resolved-changed"
        systemctl restart systemd-resolved; sleep 1; CHANGED=1; say "resolved: +DNS$add -> $(resolved_servers)"
      fi
    fi
  fi
  # the system stub symlink stays as it is (plan 239: stub hosts unchanged); anything else -> static file to the stub
  if [ "$(owner)" != resolved-stub ] && ! points_local_ok 127.0.0.53; then
    local own_up; own_up=$(alive $(own_ns))
    fb=$(first_n 2 "$(uniq_words "$own_up $(alive $PUBLIC4)")")
    point_local "$(owner)" 127.0.0.53 "$fb" || return 1
  fi
  [ "$DRY" = 1 ] || probe_ok 127.0.0.53 "$PROBE_NAME" 3 || { REASON="systemd-resolved 不应答"; return 1; }
}

# ---------------------------------------------------------------- state / check
write_state() { # only when something changed, in minimal mode (reason for the panel), or to correct a stale state
  [ "$DRY" = 1 ] && return
  if [ "$CHANGED" = 0 ] && [ "$MODE" != minimal ]; then
    [ -f "$STATE" ] || return 0
    [ "$(state_get mode)" = "$MODE" ] && [ "$(state_get reason)" = "$REASON" ] && return 0
  fi
  if [ "$MODE" = minimal ] && [ -f "$STATE" ] && [ "$(state_get mode)" = minimal ] && [ "$(state_get reason)" = "$REASON" ] && [ "$CHANGED" = 0 ]; then return 0; fi
  mkdir -p "$DIR"
  { echo "version=$SCRIPT_VERSION"; echo "mode=$MODE"; echo "reason=$REASON"; echo "zones=$(want_zones)"; echo "ts=$(date +%s)"; } > "$STATE.new" && mv -f "$STATE.new" "$STATE"
}
state_get() { sed -n "s/^$1=//p" "$STATE" 2>/dev/null | head -1; }
# check: the agent's rule set (keep in sync with go-gost/x/dnscache/check.go)
do_check() {
  local f n m r ok
  n=$(ns_list); f=$(first_ns)
  if [ ! -e /etc/resolv.conf ]; then m=none ok=0 r="没有 /etc/resolv.conf"
  elif [ -z "$f" ]; then m=none ok=0 r="resolv.conf 里没有 nameserver"
  elif [ "$f" = 127.0.0.1 ] || [ "$f" = ::1 ]; then
    if ours_running; then m=dnsmasq ok=1 r=""
    elif [ "$(state_get mode)" = minimal ]; then m=minimal ok=1 r=$(state_get reason)
    else m=dnsmasq ok=0 r="本机缓存 flvx-dnscache 未运行"; fi
  elif [ "$f" = 127.0.0.53 ]; then
    if pgrep -x systemd-resolve >/dev/null 2>&1 || pgrep -x systemd-resolved >/dev/null 2>&1; then m=resolved ok=1 r=""
    else m=resolved ok=0 r="systemd-resolved 未运行"; fi
  elif [ "$(count "$n")" -ge 2 ] && has_timeout_opt; then
    m=minimal ok=1 r=$(state_get reason); [ -n "$r" ] || r="未装本机缓存（只有备用解析器 + 1 秒超时）"
  else
    m=none ok=0
    if [ "$(count "$n")" -lt 2 ]; then r="未启用本机 DNS 缓存（只有 1 个解析器）"; else r="未启用本机 DNS 缓存（解析超时仍是 5 秒）"; fi
  fi
  echo "mode=$m ok=$ok reason=$r"
}

# ---------------------------------------------------------------- commands
cmd_ensure() {
  is_root || { echo "need root"; return 1; }
  exec 8>"$LOCK"; flock -w 120 8 || { echo "another flvx-dns-cache run is busy"; return 1; }
  local own conflict ma
  own=$(owner); ma=$(mem_avail)
  say "host $(hostname -s 2>/dev/null) owner=$own ns=[$(ns_list)] mem_avail=${ma}MB"
  if ours_present && { [ "$(first_ns)" = 127.0.0.1 ] || svc_active "$UNIT"; }; then MODE=dnsmasq
  elif resolved_stub_up && case "$own" in resolved-*|static|missing) true ;; *) false ;; esac; then MODE=resolved
  else
    MODE=dnsmasq
    conflict=$(port53_conflict)
    if [ -n "$conflict" ]; then MODE=minimal REASON="53 端口已被 ${conflict#* } 占用"
    elif pkg_ok dnsmasq && { svc_enabled dnsmasq || svc_active dnsmasq; }; then MODE=minimal REASON="系统自带 dnsmasq 服务已启用"
    elif [ ! -d /run/systemd/system ]; then MODE=minimal REASON="不是 systemd 系统"
    elif [ "$ma" -lt "$MIN_MEM_MB" ]; then MODE=minimal REASON="可用内存 ${ma} MB（低于 ${MIN_MEM_MB} MB）"
    else case "$own" in nm|symlink:*) MODE=minimal REASON="resolv.conf 由 ${own#symlink:} 管理" ;; esac
    fi
  fi
  if [ "$MODE" = dnsmasq ] && ! ensure_dnsmasq; then
    say "cache not possible: $REASON -> minimal"; MODE=minimal
  fi
  if [ "$MODE" = resolved ] && ! ensure_resolved; then
    say "resolved stub not usable: $REASON -> minimal"; MODE=minimal
  fi
  [ "$MODE" = minimal ] && to_minimal
  write_state
  local c; c=$(do_check)
  if [ "$DRY" = 1 ]; then echo "DRY-RUN: would end in mode $MODE${REASON:+ ($REASON)}; now: $c"; echo "RESULT mode=$MODE changed=0 dry_run=1 $c"; return 0; fi
  case "$MODE" in
    dnsmasq) echo "DNS 缓存：本机 dnsmasq（127.0.0.1，上游 $(conf_upstreams)）$([ $CHANGED = 1 ] && echo '已配置' || echo '无需改动')" ;;
    resolved) echo "DNS 缓存：systemd-resolved（127.0.0.53，上游 $(resolved_servers)）$([ $CHANGED = 1 ] && echo '已配置' || echo '无需改动')" ;;
    minimal) echo "DNS 缓存：精简模式（$REASON），解析器 $(ns_list)，超时 1 秒" ;;
  esac
  echo "RESULT mode=$MODE changed=$CHANGED $c"
  case "$c" in *"ok=1"*) return 0 ;; esac
  [ "$DRY" = 1 ] && return 0
  return 1
}
cmd_status() {
  echo "check:    $(do_check)"
  echo "owner:    $(owner) -> $(readlink -f /etc/resolv.conf 2>/dev/null)"
  echo "resolv:   $(grep -v '^#' /etc/resolv.conf 2>/dev/null | tr '\n' ';')"
  echo "cache:    unit=$(systemctl is-active $UNIT 2>/dev/null)/$(systemctl is-enabled $UNIT 2>/dev/null) upstreams=[$(conf_upstreams)] zones=[$(conf_zones)] -> [$(conf_ourns)] rss_kb=$(ps -o rss= -C dnsmasq 2>/dev/null | tr '\n' ' ')"
  echo "resolved: $(systemctl is-active systemd-resolved 2>/dev/null) [$(resolved_servers)]"
  echo "port53:   $(listeners53 | tr '\n' ';')"
  echo "state:    $( [ -f "$STATE" ] && tr '\n' ' ' < "$STATE")"
  pick_bk; echo "backup:   $BK $( [ -x "$BK/rollback.sh" ] && echo '(rollback.sh ready)' || echo '(none)')"
}
cmd_rollback() {
  is_root || { echo "need root"; return 1; }
  pick_bk
  [ -x "$BK/rollback.sh" ] || { echo "no rollback point ($BK/rollback.sh missing): nothing was changed on this host"; return 1; }
  "$BK/rollback.sh" "${1:-all}"
  rm -f "$STATE"
}

main() {
  local cmd=${1:-}; shift || true
  while [ $# -gt 0 ]; do
    case "$1" in
      --zones) ZONES_ARG="${ZONES_ARG} ${2:-}"; shift 2 ;;
      --zones=*) ZONES_ARG="${ZONES_ARG} ${1#--zones=}"; shift ;;
      --dry-run) DRY=1; shift ;;
      --quiet) QUIET=1; shift ;;
      all|b) RB=$1; shift ;;
      *) echo "unknown argument: $1" >&2; return 2 ;;
    esac
  done
  ZONES_ARG=$(echo "$ZONES_ARG" | tr ',' ' ')
  case "$cmd" in
    ensure) cmd_ensure ;;
    status) cmd_status ;;
    check) do_check ;;
    rollback) cmd_rollback "${RB:-all}" ;;
    version) echo "flvx-dns-cache $SCRIPT_VERSION" ;;
    *) sed -n '2,12p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//'; return 2 ;;
  esac
}
main "$@"
