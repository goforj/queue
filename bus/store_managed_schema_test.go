package bus

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestNewSQLStoreWithManagedSchemaForwardsWithoutDDL proves the deprecated bus
// constructor retains the root caller-managed schema behavior.
func TestNewSQLStoreWithManagedSchemaForwardsWithoutDDL(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "bus-managed-empty.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := NewSQLStoreWithManagedSchema(SQLStoreConfig{DB: db, DriverName: "sqlite"})
	if err != nil {
		t.Fatalf("new managed-schema store: %v", err)
	}
	if _, err := store.GetChain(ctx, "missing"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("unprovisioned managed schema error = %v", err)
	}
	var tableCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE 'bus_%'`).Scan(&tableCount); err != nil {
		t.Fatalf("count workflow tables: %v", err)
	}
	if tableCount != 0 {
		t.Fatalf("deprecated managed-schema constructor created %d workflow tables", tableCount)
	}
}
