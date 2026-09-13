# mmwx-custom

`mmwx-custom` contains the Custom UI, the Custom API, and the small same-origin
proxy that connects the UI to the configured miaomiaowuX backend.

The official release at `mmwx.imgamer.top` remains an unmodified official
miaomiaowuX UI and backend. The Custom stack is intended for the separate
development environment, such as `mmwxc.imgamer.top`.

## Boundaries

- Official UI -> official miaomiaowuX backend `/api/*`
- Custom UI -> Fork miaomiaowuX backend `/api/*`
- Custom UI -> this project `/api/custom/*`
- The official backend and official UI do not import or call this project.

## Releases

Each `mmwx-custom` release contains Linux packages named:

```text
mmwx-custom-linux-amd64.tar.gz
mmwx-custom-linux-arm64.tar.gz
checksums.txt
```

Each package includes the `mmwx-custom` executable and the matching built
Custom UI in `frontend/dist`. Consumers download these Release assets; no
generated `dist` directory is maintained in the Fork repository.

Connections Helper release assets are also published for direct installation:

```text
mmwxc-helper-linux-amd64
mmwxc-helper-linux-arm64
mmwxc-core-linux-amd64
mmwxc-core-linux-arm64
install-helper.sh
core-build-info.txt
```

`install-helper.sh` is the single idempotent installer for both a first install
and an in-place upgrade. Release checksums cover the installer, Helper, Custom
Core, packages, and Core build metadata.

## Local Build

```bash
./build.sh
```

This creates `build/mmwx-custom`. The frontend output remains
`frontend/dist` and is included only in release packages.

## Development

```bash
cd frontend
MMWX_API_TARGET=http://127.0.0.1:12891 \
MMWXC_API_TARGET=http://127.0.0.1:12890 \
npm run dev
```

## Runtime Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `MMWXC_API_LISTEN_ADDR` | `127.0.0.1:12890` | HTTP listen address |
| `MMWXC_API_TOKEN` | empty | Bearer token for operator-only Custom Agent management; management is disabled while empty |
| `MMWXC_ADMIN_DB_CONFIG` | `/etc/mmwx/data/database.json` | Read-only source for validating active admin sessions without calling the official Secure Channel API |
| `MMWXC_ALLOWED_ORIGINS` | development origins | Comma-separated CORS allowlist |
| `MMWXC_FRONTEND_DIR` | `frontend/dist` | Built Custom UI directory |
| `MMWX_API_TARGET` | `http://127.0.0.1:12891` | Fork Backend target for `/api/*` proxy |
| `MMWXC_HELPER_STATE_FILE` | `/etc/mmwx-custom/helper-state.json` | Persistent Custom server identity state |

The following endpoints are available:

- `GET /healthz`
- `GET /api/dashboard/system`
- `GET /api/custom/dashboard/system`
- `GET /api/custom/agent/metrics`
- `POST /api/custom/helper/install-token`
- `GET /api/custom/helper/install/<install-token>`
- `GET /api/custom/servers/:id/agent`
- `POST /api/custom/servers/:id/agent`
- `/api/*` proxied to `MMWX_API_TARGET`

## Connections Helper

`mmwxc-helper v0.4.5` is the Custom Agent. The normal installer defaults to a
transactional external single-Core takeover; `--helper-only` keeps the
diagnostic-only maintenance mode. The official `mmw-agent` remains responsible
for config generation and the `xray.service` lifecycle.

For an external single-Core deployment, the optional ownership mode keeps the
official Agent's `/usr/local/etc/xray/config.json` and `xray.service` lifecycle,
while a systemd drop-in pins `ExecStart` to `/opt/mmwxc/core/xray`. The Helper
repairs only binary/service ownership drift; normal Agent config writes and
Xray restarts are left untouched.

The `external.ownership.arm` management step installs that drop-in and releases
the old Custom service ports before the controller asks the official Agent to
switch modes. This keeps the Agent's asynchronous external-mode startup from
racing a second Core process.

The helper reports server-level Connections for the Custom service management
page. Its counting source matches the 3x-ui-style socket-table method by reading:

```text
/proc/net/tcp
/proc/net/tcp6
/proc/net/udp
/proc/net/udp6
```

With control interface v2, the Fork Core propagates the authenticated
`inbound_tag/user` identity into each physical outbound TCP dial and records
the exact local/remote address and port tuple before close. The Core then
matches that tuple against the kernel TCP tables, so per-user inbound and
outbound `ESTABLISHED`, `SYN_*`, `FIN_WAIT*`, `TIME_WAIT`, `CLOSE_WAIT`,
`LAST_ACK`, and `CLOSING` values are not inferred from a shared port. A socket
that has not completed protocol authentication is intentionally not assigned
to a user.

Per-user total, online-IP, outbound-active, and outbound-NEW/s limits plus the
optional global total limit are persisted by the Helper and enforced inside
the Core. They reserve/reject only new authenticated inbound or physical
outbound resources and never terminate connections that were already accepted.
The total-limit definition is authenticated inbound active + physical outbound
active + physical outbound pending; closed `TIME_WAIT` records are reported but
do not consume a limit slot.

Normal installation does not require users to type a server id or token. The
recommended flow is:

```text
Create Remote Server
-> Open that server in Service Management
-> Connections Helper
-> Generate install command
-> SSH to the target VPS and run the command
-> Helper binds to that server automatically
-> UI shows the server card 🔌 Connections
```

The server page generates a short-lived one-time install URL. Long-lived helper
tokens are not shown in the frontend or release notes.

The generated command runs the same installer on new and existing machines:

```bash
(install_script="$(mktemp)" && trap 'rm -f "$install_script"' EXIT && curl --fail --show-error --silent --location --retry 3 --output "$install_script" 'https://mmwxc.imgamer.top/api/custom/helper/install/<one-time-token>' && test -s "$install_script" && bash "$install_script")
```

The installer is downloaded completely before execution. A download failure,
empty response, checksum failure, or installer failure therefore returns a
non-zero status instead of being hidden by a `curl | bash` pipeline.

On an existing installation, `/etc/mmwxc-helper.env` and
`/var/lib/mmwxc-helper/state.json` are preserved byte-for-byte. This retains the
server identity, controller binding, token, interval, and connection-control
settings. The installer stages and verifies both binaries, keeps at most two
rollback binaries per component, and only starts the Helper. It prepares
`mmwxc-core.service`, but does not enable or start an inactive Custom Core until
an explicit configuration/cutover command is issued.

After registration, the controller can enqueue only these signed actions:

```text
helper.status helper.version helper.update
core.status core.version core.install core.update core.restart core.stop core.rollback core.config.apply
connection.status connection.settings
```

There is no remote shell, arbitrary command, arbitrary file path, upload, or
general systemd interface. Artifact URLs are HTTPS allowlisted and every binary
must pass SHA256, ELF architecture, and component version checks before atomic
replacement.

## systemd

The installed Custom service is named `mmwx-custom.service` and runs
`/usr/local/bin/mmwx-custom`. A unit template is available at
[`deploy/mmwx-custom.service`](deploy/mmwx-custom.service). The Fork
installer writes the matching service together with
`mmwx-custom-backend.service` for the development stack.

## Reverse Proxy

Point the development-domain reverse proxy to `127.0.0.1:12890`. This service
serves the Custom UI, handles `/api/custom/*`, and proxies `/api/*` to the
configured miaomiaowuX backend.

Do not commit tokens, passwords, Cloudflare credentials, cookies, private
keys, or `.env` files.
