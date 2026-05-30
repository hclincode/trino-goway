//go:build integration || e2e

package testutil

import (
	"context"
	"fmt"
	"testing"
	"time"

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
	db, _ := postgresContainer(t, true)
	return db
}

// PostgresContainerDSN starts a Postgres testcontainer and returns the DSN string.
// The container is registered for cleanup via t.Cleanup. Unlike PostgresContainer,
// no *sqlx.DB is opened — callers that just need to hand a DSN to a subprocess
// (e.g. the E2E binary harness) avoid an unused connection.
func PostgresContainerDSN(t testing.TB) string {
	t.Helper()
	_, dsn := postgresContainer(t, false)
	return dsn
}

func postgresContainer(t testing.TB, openDB bool) (*sqlx.DB, string) {
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
			// Postgres logs "database system is ready to accept connections" twice
			// during startup — once on internal init, again after the TCP listener
			// binds. Requiring two occurrences avoids handing back a DSN before the
			// listener accepts external connections (otherwise subprocesses hit
			// "connect: EOF" intermittently on first dial).
			WaitingFor: wait.ForAll(
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
				wait.ForListeningPort("5432/tcp").WithStartupTimeout(60*time.Second),
			),
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

	if !openDB {
		t.Cleanup(func() {
			if err := container.Terminate(context.Background()); err != nil {
				t.Errorf("testutil: PostgresContainer cleanup: terminate container: %v", err)
			}
		})
		return nil, dsn
	}

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

	return db, dsn
}
