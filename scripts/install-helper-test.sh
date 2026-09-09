#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GO_BIN="${GO_BIN:-$(command -v go || true)}"
if [[ -z "$GO_BIN" && -x /usr/local/go/bin/go ]]; then GO_BIN=/usr/local/go/bin/go; fi
[[ -n "$GO_BIN" ]] || { echo "go is required" >&2; exit 1; }

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported test architecture" >&2; exit 1 ;;
esac

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
assets="$tmp/assets"
mkdir -p "$assets"

cat >"$tmp/helper.go" <<'EOF'
package main
import ("fmt"; "os")
func main() { if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version") { fmt.Println("mmwxc-helper v0.3.1"); return } }
EOF
cat >"$tmp/core.go" <<'EOF'
package main
import ("fmt"; "time")
func main() {
	fmt.Println("Xray 26.9.8 MMWXC test")
	time.Sleep(100 * time.Millisecond)
	fmt.Println("MMWXC version probe trailer")
}
EOF
CGO_ENABLED=0 "$GO_BIN" build -trimpath -o "$assets/mmwxc-helper-linux-$ARCH" "$tmp/helper.go"
CGO_ENABLED=0 "$GO_BIN" build -trimpath -o "$assets/mmwxc-core-linux-$ARCH" "$tmp/core.go"
(
  cd "$assets"
  sha256sum "mmwxc-helper-linux-$ARCH" "mmwxc-core-linux-$ARCH" >checksums.txt
)

# Deterministically reproduce the old validation bug: grep -q accepts the first
# line and closes the pipe before the Core writes its trailer.
legacy_pipeline_status=0
"$assets/mmwxc-core-linux-$ARCH" version | grep -qi '^Xray ' || legacy_pipeline_status=$?
[[ "$legacy_pipeline_status" -eq 141 ]] || {
  echo "test fixture did not reproduce the legacy SIGPIPE (got $legacy_pipeline_status)" >&2
  exit 1
}

legacy_root="$tmp/legacy-root"
mkdir -p "$legacy_root/etc" "$legacy_root/usr/local/bin" "$legacy_root/var/lib/mmwxc-helper"
cat >"$legacy_root/etc/mmwxc-helper.env" <<'EOF'
MMWXC_HELPER_API_URL=https://controller.invalid
MMWXC_HELPER_SERVER_ID=preserved-server-id
MMWXC_HELPER_TOKEN=preserved-token
MMWXC_HELPER_INTERVAL=9s
EOF
cat >"$legacy_root/usr/local/bin/mmwxc-helper" <<'EOF'
#!/usr/bin/env bash
echo "mmwxc-helper v0.1.0"
EOF
chmod 0755 "$legacy_root/usr/local/bin/mmwxc-helper"
cat >"$legacy_root/var/lib/mmwxc-helper/state.json" <<'EOF'
{"settings":{"online_ip_grace_period_seconds":45,"users":[]}}
EOF
config_before="$(sha256sum "$legacy_root/etc/mmwxc-helper.env" | awk '{print $1}')"
state_before="$(sha256sum "$legacy_root/var/lib/mmwxc-helper/state.json" | awk '{print $1}')"

legacy_output="$(MMWXC_INSTALL_ROOT="$legacy_root" MMWXC_ASSET_DIR="$assets" "$ROOT_DIR/scripts/install-helper.sh")"
grep -q '^\[mmwxc\] Custom Agent installer started$' <<<"$legacy_output"
grep -q '^\[mmwxc\] Existing Helper identity/config detected; preserving it unchanged$' <<<"$legacy_output"
grep -q '^\[mmwxc\] Helper version: v0.1.0 -> v0.3.1$' <<<"$legacy_output"
grep -q '^\[mmwxc\] MMWXC Custom Agent installation complete$' <<<"$legacy_output"
[[ "$($legacy_root/usr/local/bin/mmwxc-helper --version)" == "mmwxc-helper v0.3.1" ]]
[[ "$config_before" == "$(sha256sum "$legacy_root/etc/mmwxc-helper.env" | awk '{print $1}')" ]]
[[ "$state_before" == "$(sha256sum "$legacy_root/var/lib/mmwxc-helper/state.json" | awk '{print $1}')" ]]
[[ -f "$legacy_root/etc/systemd/system/mmwxc-helper.service" ]]
[[ -f "$legacy_root/etc/systemd/system/mmwxc-core.service" ]]
[[ ! -e "$legacy_root/etc/mmwxc/core/config.json" ]]
installed_core_output="$("$legacy_root/opt/mmwxc/core/xray" version)"
[[ "${installed_core_output%%$'\n'*}" == "Xray 26.9.8 MMWXC test" ]]

MMWXC_INSTALL_ROOT="$legacy_root" MMWXC_ASSET_DIR="$assets" "$ROOT_DIR/scripts/install-helper.sh" >/dev/null
[[ "$(find "$legacy_root/var/lib/mmwxc/rollback/helper" -type f | wc -l)" -le 2 ]]
[[ "$(find "$legacy_root/var/lib/mmwxc/rollback/core" -type f 2>/dev/null | wc -l)" -le 2 ]]

fresh_root="$tmp/fresh-root"
MMWXC_INSTALL_ROOT="$fresh_root" MMWXC_ASSET_DIR="$assets" \
  MMWXC_HELPER_API_URL=https://controller.invalid \
  MMWXC_HELPER_SERVER_ID=new-server-id \
  MMWXC_HELPER_TOKEN=new-token \
  "$ROOT_DIR/scripts/install-helper.sh" >/dev/null
grep -q '^MMWXC_HELPER_SERVER_ID=new-server-id$' "$fresh_root/etc/mmwxc-helper.env"
grep -q '^MMWXC_HELPER_TOKEN=new-token$' "$fresh_root/etc/mmwxc-helper.env"
[[ -x "$fresh_root/usr/local/bin/mmwxc-helper" ]]
[[ -x "$fresh_root/opt/mmwxc/core/xray" ]]

bad_assets="$tmp/bad-assets"
cp -a "$assets" "$bad_assets"
printf '0%.0s' {1..64} >"$bad_assets/checksums.txt"
printf '  mmwxc-helper-linux-%s\n' "$ARCH" >>"$bad_assets/checksums.txt"
sha256sum "$bad_assets/mmwxc-core-linux-$ARCH" >>"$bad_assets/checksums.txt"
bad_root="$tmp/bad-root"
bad_output=""
if bad_output="$(MMWXC_INSTALL_ROOT="$bad_root" MMWXC_ASSET_DIR="$bad_assets" \
  MMWXC_HELPER_API_URL=https://controller.invalid MMWXC_HELPER_SERVER_ID=id MMWXC_HELPER_TOKEN=token \
  "$ROOT_DIR/scripts/install-helper.sh" 2>&1)"; then
  echo "installer accepted an invalid checksum" >&2
  exit 1
fi
grep -Fq "checksum mismatch for mmwxc-helper-linux-$ARCH" <<<"$bad_output"
[[ ! -e "$bad_root/usr/local/bin/mmwxc-helper" ]]
[[ ! -e "$bad_root/opt/mmwxc/core/xray" ]]

write_checksums() {
  local directory="$1"
  (
    cd "$directory"
    sha256sum "mmwxc-helper-linux-$ARCH" "mmwxc-core-linux-$ARCH" >checksums.txt
  )
}

assert_verification_rejected() {
  local name="$1" asset_directory="$2" expected_error="$3" install_root output
  install_root="$tmp/$name-root"
  output=""
  if output="$(MMWXC_INSTALL_ROOT="$install_root" MMWXC_ASSET_DIR="$asset_directory" \
    MMWXC_HELPER_API_URL=https://controller.invalid MMWXC_HELPER_SERVER_ID=id MMWXC_HELPER_TOKEN=token \
    "$ROOT_DIR/scripts/install-helper.sh" 2>&1)"; then
    echo "installer accepted invalid $name assets" >&2
    exit 1
  fi
  grep -Fq "$expected_error" <<<"$output"
  [[ ! -e "$install_root/usr/local/bin/mmwxc-helper" ]]
  [[ ! -e "$install_root/opt/mmwxc/core/xray" ]]
}

non_elf_assets="$tmp/non-elf-assets"
cp -a "$assets" "$non_elf_assets"
printf '%s\n' 'not an ELF binary' >"$non_elf_assets/mmwxc-core-linux-$ARCH"
chmod 0755 "$non_elf_assets/mmwxc-core-linux-$ARCH"
write_checksums "$non_elf_assets"
assert_verification_rejected non-elf "$non_elf_assets" "artifact is not an ELF binary"

wrong_arch_assets="$tmp/wrong-arch-assets"
cp -a "$assets" "$wrong_arch_assets"
if [[ "$ARCH" == amd64 ]]; then wrong_machine='\267\000'; else wrong_machine='\076\000'; fi
printf '%b' "$wrong_machine" | dd of="$wrong_arch_assets/mmwxc-core-linux-$ARCH" bs=1 seek=18 count=2 conv=notrunc status=none
write_checksums "$wrong_arch_assets"
assert_verification_rejected wrong-architecture "$wrong_arch_assets" "artifact architecture mismatch"

cat >"$tmp/bad-version-core.go" <<'EOF'
package main
import "fmt"
func main() { fmt.Println("unexpected core version") }
EOF
bad_version_assets="$tmp/bad-version-assets"
cp -a "$assets" "$bad_version_assets"
CGO_ENABLED=0 "$GO_BIN" build -trimpath -o "$bad_version_assets/mmwxc-core-linux-$ARCH" "$tmp/bad-version-core.go"
write_checksums "$bad_version_assets"
assert_verification_rejected bad-version "$bad_version_assets" "invalid Custom Core version output"

echo "installer tests passed"
