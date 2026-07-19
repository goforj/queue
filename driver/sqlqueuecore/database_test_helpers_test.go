package sqlqueuecore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
)

type databaseConnectorStub struct {
	conn *databaseConnStub
}

type databaseDriverStub struct {
	conn *databaseConnStub
}

type databaseConnStub struct {
	exec        func(context.Context, string, []driver.NamedValue) (driver.Result, error)
	query       func(context.Context, string, []driver.NamedValue) (driver.Rows, error)
	beginErr    error
	commitErr   error
	rollbackErr error
	pingErr     error
	closeCalls  int
}

type databaseTxStub struct {
	conn *databaseConnStub
}

type databaseRowsStub struct {
	columns []string
	values  [][]driver.Value
	err     error
	index   int
}

// Connect exposes the scripted connection through database/sql without registering a process-global driver name.
func (c databaseConnectorStub) Connect(context.Context) (driver.Conn, error) {
	return c.conn, nil
}

// Driver returns the connector's fallback driver implementation.
func (c databaseConnectorStub) Driver() driver.Driver {
	return databaseDriverStub{conn: c.conn}
}

// Open returns the scripted connection when database/sql uses the fallback driver path.
func (d databaseDriverStub) Open(string) (driver.Conn, error) {
	return d.conn, nil
}

// Prepare rejects fallback statement preparation because every test scripts context-aware operations directly.
func (c *databaseConnStub) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected database statement preparation")
}

// Close records database ownership behavior without invalidating the reusable script fixture.
func (c *databaseConnStub) Close() error {
	c.closeCalls++
	return nil
}

// Begin starts a transaction through the context-aware implementation.
func (c *databaseConnStub) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

// BeginTx returns a transaction that delegates completion behavior to the scripted connection.
func (c *databaseConnStub) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.beginErr != nil {
		return nil, c.beginErr
	}
	return databaseTxStub{conn: c}, nil
}

// Ping returns the configured connectivity result.
func (c *databaseConnStub) Ping(context.Context) error {
	return c.pingErr
}

// ExecContext delegates execution to the test-specific script.
func (c *databaseConnStub) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.exec == nil {
		return nil, errors.New("unexpected database execution")
	}
	return c.exec(ctx, query, args)
}

// QueryContext delegates queries to the test-specific script.
func (c *databaseConnStub) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.query == nil {
		return nil, errors.New("unexpected database query")
	}
	return c.query(ctx, query, args)
}

// Commit returns the configured transaction completion result.
func (t databaseTxStub) Commit() error {
	return t.conn.commitErr
}

// Rollback returns the configured transaction rollback result.
func (t databaseTxStub) Rollback() error {
	return t.conn.rollbackErr
}

// Columns returns the shape expected by Scan.
func (r *databaseRowsStub) Columns() []string {
	return r.columns
}

// Close releases no resources because all rows are in-memory fixtures.
func (r *databaseRowsStub) Close() error {
	return nil
}

// Next returns scripted rows followed by an optional terminal iteration error.
func (r *databaseRowsStub) Next(dest []driver.Value) error {
	if r.index < len(r.values) {
		copy(dest, r.values[r.index])
		r.index++
		return nil
	}
	if r.err != nil {
		err := r.err
		r.err = nil
		return err
	}
	return io.EOF
}

// newDatabaseStub opens a database/sql handle backed by one deterministic scripted connection.
func newDatabaseStub(conn *databaseConnStub) *sql.DB {
	return sql.OpenDB(databaseConnectorStub{conn: conn})
}

// databaseCountRows returns one integer result suitable for COUNT queries.
func databaseCountRows(count int64) driver.Rows {
	return &databaseRowsStub{
		columns: []string{"count"},
		values:  [][]driver.Value{{count}},
	}
}
