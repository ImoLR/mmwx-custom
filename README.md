# mmwx-custom

Standalone Custom UI and Custom API project for 妙妙屋X.

This repository contains the Custom UI and the small read-only Custom API used
by that UI. The official miaomiaowuX fork remains responsible for the official
backend and official embedded UI.

## Structure

```text
frontend/       Custom UI
main.go         Custom API and same-origin proxy service
deploy/         Deployment examples
docs/           Project notes
```

## Request Boundaries

- Official UI -> official miaomiaowuX backend `/api/*`
- Custom UI -> official miaomiaowuX backend `/api/*`
- Custom UI -> this service `/api/custom/*`
- Official backend must not call this Custom API
- Official UI must not call `/api/custom/*`

## Features

- Serves the built Custom UI from `frontend/dist`.
- Proxies Custom UI `/api/*` requests to the official miaomiaowuX backend.
- Provides host-level system metrics for Custom UI.
- Does not connect to the miaomiaowuX database.
- Does not depend on miaomiaowuX internal Go packages.
- Does not modify official miaomiaowuX business APIs.
- Listens on a local address by default.

## Endpoints

- `GET /healthz`
- `GET /api/dashboard/system`
- `GET /api/custom/dashboard/system`
- `/api/*` proxied to the configured official miaomiaowuX backend

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `MMWXC_API_LISTEN_ADDR` | `127.0.0.1:12890` | HTTP listen address |
| `MMWXC_API_TOKEN` | empty | Optional bearer token for `/api/dashboard/system` |
| `MMWXC_ALLOWED_ORIGINS` | `http://178.214.214.173:5173,https://dev.mmwx.imgamer.top` | Comma-separated CORS allowlist |
| `MMWXC_FRONTEND_DIR` | `frontend/dist` | Built Custom UI directory served by this service |
| `MMWX_API_TARGET` | `https://mmwx.imgamer.top` | Official miaomiaowuX backend target for `/api/*` proxy |

When `MMWXC_API_TOKEN` is set, requests to `/api/dashboard/system` must include:

```text
Authorization: Bearer <token>
```

Do not commit real tokens, passwords, Cloudflare credentials, cookies, private
keys, or `.env` files.

## Build

```bash
./build.sh
```

## Run

```bash
MMWXC_API_LISTEN_ADDR=127.0.0.1:12890 ./build/mmwx-custom-api
```

## Frontend Development

```bash
cd frontend
MMWX_API_TARGET=https://mmwx.imgamer.top MMWXC_API_TARGET=http://127.0.0.1:12890 npm run dev
```

## systemd Example

```ini
[Unit]
Description=miaomiaowuX Custom UI API
After=network.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=/usr/local/bin/mmwx-custom-api
Restart=on-failure
RestartSec=5
Environment="MMWXC_API_LISTEN_ADDR=127.0.0.1:12890"
Environment="MMWXC_FRONTEND_DIR=/opt/mmwx-custom/frontend/dist"
Environment="MMWX_API_TARGET=https://mmwx.imgamer.top"
Environment="MMWXC_ALLOWED_ORIGINS=http://178.214.214.173:5173,https://dev.mmwx.imgamer.top"

NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

If you configure `MMWXC_API_TOKEN`, keep it outside Git, for example in a
root-owned environment file. Do not put `MMWXC_API_TOKEN` into frontend code.

## Reverse Proxy

```text
Cloudflare Tunnel
        |
mmwx-custom: http://127.0.0.1:12890
        |
Custom UI + /api/custom/* + /api/* proxy
```

See `deploy/nginx.example.conf` for a minimal nginx example.
