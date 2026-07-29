# Architecture

`mmwx-custom` is intentionally separate from the official miaomiaowuX fork.

The Custom UI runs from this repository and uses two same-origin API areas:

- `/api/*` is proxied to the official miaomiaowuX backend.
- `/api/custom/*` is handled by this repository's Custom API.

The official fork must not import this repository, proxy to it, embed its
frontend, or expose `/api/custom/*` as part of the official UI contract.
