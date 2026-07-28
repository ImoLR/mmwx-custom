# mmwx-custom-api

Standalone custom API service for 妙妙屋X Custom UI.

This service provides small, read-only custom APIs for Custom UI while keeping
the official miaomiaowuX backend and business APIs decoupled.

## Features

- Provides host-level system metrics for Custom UI.
- Does not connect to the miaomiaowuX database.
- Does not depend on miaomiaowuX internal Go packages.
- Does not modify official miaomiaowuX business APIs.
- Listens on a local address by default.

## Endpoints

- `GET /healthz`
- `GET /api/dashboard/system`

## Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `MMWXC_API_LISTEN_ADDR` | `127.0.0.1:12890` | HTTP listen address |
| `MMWXC_API_TOKEN` | empty | Optional bearer token for `/api/dashboard/system` |
| `MMWXC_ALLOWED_ORIGINS` | `http://178.214.214.173:5173,https://dev.mmwx.imgamer.top` | Comma-separated CORS allowlist |

When `MMWXC_API_TOKEN` is set, requests to `/api/dashboard/system` must include:

```text
Authorization: Bearer <token>
```

Do not commit real tokens, passwords, Cloudflare credentials, cookies, private
keys, or `.env` files.

## Build

```bash
go mod tidy
go build -o mmwx-custom-api .
```

## Run

```bash
MMWXC_API_LISTEN_ADDR=127.0.0.1:12890 ./mmwx-custom-api
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
Environment="MMWXC_ALLOWED_ORIGINS=http://178.214.214.173:5173,https://dev.mmwx.imgamer.top"

NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

If you configure `MMWXC_API_TOKEN`, keep it outside Git, for example in a
root-owned environment file. Do not put `MMWXC_API_TOKEN` into frontend code.

## Cloudflare Tunnel

`mmwx-custom-api` does not need an independent public domain, and port `12890`
should not be exposed directly to the internet.

Recommended path:

```text
Cloudflare Tunnel
        |
Custom UI: http://127.0.0.1:5173
        |
/api/custom/*
        |
Custom API: http://127.0.0.1:12890
```

Recommended Cloudflare Tunnel origin:

```text
http://127.0.0.1:5173
```

Do not point Cloudflare Tunnel directly to:

```text
http://127.0.0.1:12890
```

Custom UI should proxy same-origin `/api/custom/*` requests to this service.
