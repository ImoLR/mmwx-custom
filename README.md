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

The paired installation, update, and uninstall commands live in the Fork
repository: [ImoLR/miaomiaowuX](https://github.com/ImoLR/miaomiaowuX).

## Local Build

```bash
./build.sh
```

This creates `build/mmwx-custom`. The frontend output remains
`frontend/dist` and is included only in release packages.

## Development

```bash
cd frontend
MMWX_API_TARGET=https://mmwx.imgamer.top \
MMWXC_API_TARGET=http://127.0.0.1:12890 \
npm run dev
```

## Runtime Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `MMWXC_API_LISTEN_ADDR` | `127.0.0.1:12890` | HTTP listen address |
| `MMWXC_API_TOKEN` | empty | Optional bearer token for `/api/dashboard/system` |
| `MMWXC_ALLOWED_ORIGINS` | development origins | Comma-separated CORS allowlist |
| `MMWXC_FRONTEND_DIR` | `frontend/dist` | Built Custom UI directory |
| `MMWX_API_TARGET` | `http://127.0.0.1:12889` | Fork Backend target for `/api/*` proxy |

The following endpoints are available:

- `GET /healthz`
- `GET /api/dashboard/system`
- `GET /api/custom/dashboard/system`
- `/api/*` proxied to `MMWX_API_TARGET`

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
