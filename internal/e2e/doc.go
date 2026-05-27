// Package e2e contains end-to-end QA gate tests that boot real upstream
// dependencies (Trino, Postgres, etc.) via testcontainers-go.
//
// Tests in this package are gated behind the `e2e` build tag and run with:
//
//	go test -tags e2e -timeout 120s ./internal/e2e/...
//
// They silently skip when Docker is unavailable so they remain safe to invoke
// from contributor laptops and CI lanes that do not provision Docker.
package e2e
