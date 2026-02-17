package queue

import (
	"database/sql"
	"testing"
)

func resetQueueTables(t *testing.T, cfg DatabaseConfig) {
	t.Helper()
	if cfg.DriverName == "" || cfg.DSN == "" {
		return
	}
	if cfg.DriverName == "sqlite" {
		// SQLite file-based integration tests can hold transient locks while the
		// worker loop is active. Use isolated DB files instead of cross-connection
		// truncate/reset to avoid SQLITE_BUSY flakiness.
		return
	}
	db, err := sql.Open(cfg.DriverName, cfg.DSN)
	if err != nil {
		t.Fatalf("open db for reset failed: %v", err)
	}
	defer db.Close()

	var stmts []string
	switch cfg.DriverName {
	case "pgx", "postgres":
		stmts = []string{
			"TRUNCATE TABLE queue_jobs RESTART IDENTITY",
			"TRUNCATE TABLE queue_unique_locks",
		}
	case "mysql":
		stmts = []string{
			"TRUNCATE TABLE queue_jobs",
			"TRUNCATE TABLE queue_unique_locks",
		}
	default:
		stmts = []string{
			"DELETE FROM queue_jobs",
			"DELETE FROM queue_unique_locks",
		}
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("reset queue tables failed: %v", err)
		}
	}
}
