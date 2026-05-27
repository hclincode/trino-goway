//go:build integration

package testutil

import (
	"context"
	"fmt"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PostgresContainer starts a Postgres testcontainer and returns a ready *sqlx.DB.
// Calls t.Cleanup to terminate the container and close the DB.
// Uses a randomized port assigned by testcontainers-go via MappedPort.
func PostgresContainer(t testing.TB) *sqlx.DB {
	t.Helper()

	ctx := context.Background()

	req := testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:16-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_USER":     "testuser",
				"POSTGRES_PASSWORD": "testpass",
				"POSTGRES_DB":       "testdb",
			},
			WaitingFor: wait.ForLog("database system is ready to accept connections"),
		},
		Started: true,
	}

	container, err := testcontainers.GenericContainer(ctx, req)
	if err != nil {
		t.Fatalf("testutil: PostgresContainer: start container: %v", err)
	}

	port, err := container.MappedPort(ctx, "5432")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("testutil: PostgresContainer: mapped port: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("testutil: PostgresContainer: host: %v", err)
	}

	dsn := fmt.Sprintf("host=%s port=%s user=testuser password=testpass dbname=testdb sslmode=disable",
		host, port.Port())

	db, err := sqlx.Open("postgres", dsn)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("testutil: PostgresContainer: open db: %v", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		_ = container.Terminate(ctx)
		t.Fatalf("testutil: PostgresContainer: ping: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("testutil: PostgresContainer cleanup: close db: %v", err)
		}
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("testutil: PostgresContainer cleanup: terminate container: %v", err)
		}
	})

	return db
}
