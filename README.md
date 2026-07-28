# mmwx-custom-api

Independent extension API for custom-ui. It exposes host-level read-only metrics
without linking to the main miaomiaowuX backend or database.

## Endpoints

- `GET /healthz`
- `GET /api/dashboard/system`

## Configuration

- `MMWXC_LISTEN_ADDR`, default `127.0.0.1:12890`
- `MMWXC_ALLOWED_ORIGINS`, comma-separated CORS allowlist
- `MMWXC_API_TOKEN`, optional bearer token

When `MMWXC_API_TOKEN` is set, requests to `/api/dashboard/system` must include:

```text
Authorization: Bearer <token>
```
