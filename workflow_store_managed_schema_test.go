package queue

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestNewSQLStoreWithManagedSchemaDoesNotCreateTables proves the explicit
// caller-managed path performs no startup DDL, even on first use.
func TestNewSQLStoreWithManagedSchemaDoesNotCreateTables(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "managed-empty.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := NewSQLStoreWithManagedSchema(SQLStoreConfig{DB: db, DriverName: "sqlite"})
	if err != nil {
		t.Fatalf("new managed-schema store: %v", err)
	}
	if _, err := store.GetChain(ctx, "missing"); err == nil || errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("unprovisioned managed schema error = %v", err)
	}
	var tableCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE 'bus_%'`).Scan(&tableCount); err != nil {
		t.Fatalf("count workflow tables: %v", err)
	}
	if tableCount != 0 {
		t.Fatalf("managed-schema constructor created %d workflow tables", tableCount)
	}
}

// TestNewSQLStoreWithManagedSchemaUsesProvisionedTables proves disabling DDL
// changes only schema ownership, not workflow-store behavior.
func TestNewSQLStoreWithManagedSchemaUsesProvisionedTables(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "managed-provisioned.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	bootstrap, err := NewSQLStore(SQLStoreConfig{DB: db, DriverName: "sqlite"})
	if err != nil {
		t.Fatalf("new bootstrap store: %v", err)
	}
	if _, err := bootstrap.GetChain(ctx, "missing"); !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("bootstrap schema: %v", err)
	}
	managed, err := NewSQLStoreWithManagedSchema(SQLStoreConfig{DB: db, DriverName: "sqlite"})
	if err != nil {
		t.Fatalf("new managed-schema store: %v", err)
	}
	if err := managed.CreateChain(ctx, ChainRecord{ChainID: "managed-chain", Nodes: []ChainNode{{NodeID: "managed-node"}}}); err != nil {
		t.Fatalf("create chain in provisioned schema: %v", err)
	}
	state, err := managed.GetChain(ctx, "managed-chain")
	if err != nil {
		t.Fatalf("get chain from provisioned schema: %v", err)
	}
	if state.ChainID != "managed-chain" || len(state.Nodes) != 1 || state.Nodes[0].NodeID != "managed-node" {
		t.Fatalf("managed-schema chain = %+v", state)
	}
}
