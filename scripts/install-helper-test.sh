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
import "fmt"
func main() { fmt.Println("Xray 26.9.8 MMWXC test") }
EOF
CGO_ENABLED=0 "$GO_BIN" build -trimpath -o "$assets/mmwxc-helper-linux-$ARCH" "$tmp/helper.go"
CGO_ENABLED=0 "$GO_BIN" build -trimpath -o "$assets/mmwxc-core-linux-$ARCH" "$tmp/core.go"
(
  cd "$assets"
  sha256sum "mmwxc-helper-linux-$ARCH" "mmwxc-core-linux-$ARCH" >checksums.txt
)

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
[[ "$($legacy_root/opt/mmwxc/core/xray version)" == "Xray 26.9.8 MMWXC test" ]]
[[ "$config_before" == "$(sha256sum "$legacy_root/etc/mmwxc-helper.env" | awk '{print $1}')" ]]
[[ "$state_before" == "$(sha256sum "$legacy_root/var/lib/mmwxc-helper/state.json" | awk '{print $1}')" ]]
[[ -f "$legacy_root/etc/systemd/system/mmwxc-helper.service" ]]
[[ -f "$legacy_root/etc/systemd/system/mmwxc-core.service" ]]
[[ ! -e "$legacy_root/etc/mmwxc/core/config.json" ]]

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
if MMWXC_INSTALL_ROOT="$bad_root" MMWXC_ASSET_DIR="$bad_assets" \
  MMWXC_HELPER_API_URL=https://controller.invalid MMWXC_HELPER_SERVER_ID=id MMWXC_HELPER_TOKEN=token \
  "$ROOT_DIR/scripts/install-helper.sh" >/dev/null 2>&1; then
  echo "installer accepted an invalid checksum" >&2
  exit 1
fi
[[ ! -e "$bad_root/usr/local/bin/mmwxc-helper" ]]

echo "installer tests passed"
