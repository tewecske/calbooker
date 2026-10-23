//go:build integration

package postgres

import (
	"context"
	"testing"
)

func TestHarnessUsesIsolatedSchema(t *testing.T) {
	harness := New(t)
	if _, err := harness.DB.ExecContext(context.Background(), "CREATE TABLE harness_probe (id BIGINT PRIMARY KEY)"); err != nil {
		t.Fatalf("create probe table: %v", err)
	}

	var schema string
	if err := harness.DB.QueryRowContext(context.Background(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatalf("read current schema: %v", err)
	}
	if schema != harness.Schema {
		t.Fatalf("current schema = %q, want %q", schema, harness.Schema)
	}
}
