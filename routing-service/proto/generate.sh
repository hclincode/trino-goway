#!/usr/bin/env bash
# generate.sh — regenerate Go stubs from proto/*.proto.
# Run from routing-service/: ./proto/generate.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$SCRIPT_DIR")"

protoc \
  --proto_path="$SCRIPT_DIR" \
  --proto_path=/opt/homebrew/include \
  --go_out="$REPO_ROOT/routerpb" \
  --go_opt=paths=source_relative \
  --go-grpc_out="$REPO_ROOT/routerpb" \
  --go-grpc_opt=paths=source_relative \
  router.proto admin.proto

echo "Generated stubs in routerpb/"
