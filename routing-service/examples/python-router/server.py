"""Minimal Python reference implementation of the TrinoGatewayRouter service.

This is the "polyglot escape hatch" from the routing-service PRD §6.1: the
gateway speaks a stable gRPC contract (proto/router.proto), so a router can be
written in any language. Point trino-goway at this process with:

    routing:
      external:
        grpcAddr: "localhost:9001"

Routing logic here is intentionally trivial (source=airflow -> "etl", else the
default group) — it mirrors what the Go expr/script methods do, in ~30 lines, to
show the contract. Production routers add their own logic.

Run:
    pip install -r requirements.txt
    python -m grpc_tools.protoc -I../../proto \
        --python_out=. --grpc_python_out=. ../../proto/router.proto
    ROUTING_DEFAULT_GROUP=default python server.py
"""

import os
from concurrent import futures

import grpc

import router_pb2
import router_pb2_grpc


class TrinoGatewayRouter(router_pb2_grpc.TrinoGatewayRouterServicer):
    """Implements the Route RPC the gateway calls per request."""

    def __init__(self, default_group: str):
        self._default_group = default_group

    def Route(self, request, context):  # noqa: N802 (gRPC method name)
        qp = request.trino_query_properties
        # Contract (PRD §3): only decide for NEW query submissions; for polls,
        # cancels, etc. return an empty group so the gateway uses its default.
        if not qp or not qp.is_new_query_submission:
            return router_pb2.RouteResponse(routing_group="")

        # Example logic: route Airflow traffic to the ETL group; defer otherwise.
        # Returning "" lets the gateway fall back to its own default group.
        if request.trino_source == "airflow":
            return router_pb2.RouteResponse(routing_group="etl")

        # tier=premium client tag -> premium group.
        if "tier=premium" in list(request.client_tags):
            return router_pb2.RouteResponse(routing_group="premium")

        return router_pb2.RouteResponse(routing_group=self._default_group)


def serve() -> None:
    addr = os.environ.get("ROUTING_ADDR", "[::]:9001")
    default_group = os.environ.get("ROUTING_DEFAULT_GROUP", "default")

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=10))
    router_pb2_grpc.add_TrinoGatewayRouterServicer_to_server(
        TrinoGatewayRouter(default_group), server
    )
    server.add_insecure_port(addr)
    server.start()
    print(f"python reference router listening on {addr} (default={default_group})")
    server.wait_for_termination()


if __name__ == "__main__":
    serve()
