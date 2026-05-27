//go:build integration

package testutil

import (
	"context"
	"fmt"
	"testing"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// MySQLContainer starts a MySQL testcontainer and returns a ready *sqlx.DB.
// Calls t.Cleanup to terminate the container and close the DB.
func MySQLContainer(t testing.TB) *sqlx.DB {
	t.Helper()

	ctx := context.Background()

	req := testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "mysql:8-debian",
			ExposedPorts: []string{"3306/tcp"},
			Env: map[string]string{
				"MYSQL_ROOT_PASSWORD": "testpass",
				"MYSQL_DATABASE":      "testdb",
				"MYSQL_USER":          "testuser",
				"MYSQL_PASSWORD":      "testpass",
			},
			WaitingFor: wait.ForLog("ready for connections"),
		},
		Started: true,
	}

	container, err := testcontainers.GenericContainer(ctx, req)
	if err != nil {
		t.Fatalf("testutil: MySQLContainer: start container: %v", err)
	}

	port, err := container.MappedPort(ctx, "3306")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("testutil: MySQLContainer: mapped port: %v", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("testutil: MySQLContainer: host: %v", err)
	}

	dsn := fmt.Sprintf("testuser:testpass@tcp(%s:%s)/testdb?parseTime=true",
		host, port.Port())

	db, err := sqlx.Open("mysql", dsn)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("testutil: MySQLContainer: open db: %v", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		_ = container.Terminate(ctx)
		t.Fatalf("testutil: MySQLContainer: ping: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("testutil: MySQLContainer cleanup: close db: %v", err)
		}
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("testutil: MySQLContainer cleanup: terminate container: %v", err)
		}
	})

	return db
}
