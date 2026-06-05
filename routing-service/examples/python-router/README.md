# Python reference router

A minimal, runnable `grpcio` implementation of the `TrinoGatewayRouter` gRPC
service — the **polyglot escape hatch** from the routing-service PRD §6.1. The
gateway speaks a stable proto contract (`routing-service/proto/router.proto`), so
a router can be written in any language; this shows the contract in ~30 lines of
Python.

It is **not** part of the Go build — it's a self-contained example.

## Setup

```sh
cd routing-service/examples/python-router
python -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt
```

## Generate the gRPC stubs from the proto

The stubs (`router_pb2.py`, `router_pb2_grpc.py`) are generated from the same
`.proto` the Go service uses — they are **not** committed; generate them:

```sh
python -m grpc_tools.protoc \
  -I../../proto \
  --python_out=. \
  --grpc_python_out=. \
  ../../proto/router.proto
```

## Run

```sh
ROUTING_DEFAULT_GROUP=default python server.py
# listening on [::]:9001
```

Env vars:
- `ROUTING_ADDR` — listen address (default `[::]:9001`)
- `ROUTING_DEFAULT_GROUP` — group returned when no rule matches (default `default`)

## Point the gateway at it

```yaml
# trino-goway config.yaml
routing:
  external:
    grpcAddr: "localhost:9001"
    timeout: "200ms"
  defaultGroup: "default"
```

## Behaviour

Trivial example logic (mirrors the Go `expr`/`script` methods):
- non-new submissions (polls/cancels) → empty group (gateway uses its default)
- `trino_source == "airflow"` → `etl`
- client tag `tier=premium` → `premium`
- otherwise → the configured default group

The gateway falls back to `routing.defaultGroup` on any error or timeout, so this
router is never a hard dependency for query execution.
