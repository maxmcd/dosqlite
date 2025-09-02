package dosqlite

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Conn implements the database/sql/driver.Conn interface
type Conn struct {
	listener listener
	closed   bool
}

// Prepare returns a prepared statement, bound to this connection
func (c *Conn) Prepare(query string) (driver.Stmt, error) {
	return &Stmt{
		conn:  c,
		query: query,
	}, nil
}

// Close invalidates and potentially stops any current prepared statements and transactions
func (c *Conn) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	return c.listener.Close()
}

// Begin starts and returns a new transaction
func (c *Conn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("dosqlite: transactions not supported")
}

// ExecContext executes a query without returning any rows
func (c *Conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.closed {
		return nil, driver.ErrBadConn
	}

	params := make([]interface{}, len(args))
	for i, arg := range args {
		params[i] = convertDriverValueToJSON(arg.Value)
	}

	req := ExecRequest{
		Cmd:    "exec",
		SQL:    query,
		Params: params,
	}

	var resp ExecResponse
	if err := c.sendRequest(req, &resp); err != nil {
		return nil, err
	}

	if !resp.OK {
		return nil, fmt.Errorf("dosqlite: %s", resp.Error)
	}

	return &Result{}, nil
}

func (c *Conn) sendRequest(req interface{}, resp interface{}) error {
	// Marshal request to JSON
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("dosqlite: failed to marshal request: %w", err)
	}
	r, err := c.listener.Send(string(payload))
	if err != nil {
		return err
	}

	// Unmarshal response
	if err := json.Unmarshal([]byte(r), resp); err != nil {
		return fmt.Errorf("dosqlite: failed to unmarshal response: %w", err)
	}

	return nil
}

// QueryContext executes a query that returns rows
func (c *Conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.closed {
		return nil, driver.ErrBadConn
	}

	params := make([]interface{}, len(args))
	for i, arg := range args {
		params[i] = convertDriverValueToJSON(arg.Value)
	}

	req := QueryRequest{
		Cmd:    "query",
		SQL:    query,
		Params: params,
	}

	var resp QueryResponse
	if err := c.sendRequest(req, &resp); err != nil {
		return nil, err
	}

	if !resp.OK {
		return nil, fmt.Errorf("dosqlite: %s", resp.Error)
	}

	return &Rows{
		rows:    resp.Rows,
		current: -1,
	}, nil
}

// Stmt implements the database/sql/driver.Stmt interface
type Stmt struct {
	conn  *Conn
	query string
}

// Close closes the statement
func (s *Stmt) Close() error {
	return nil
}

// NumInput returns the number of placeholder parameters
func (s *Stmt) NumInput() int {
	return strings.Count(s.query, "?")
}

// Exec executes a query without returning any rows
func (s *Stmt) Exec(args []driver.Value) (driver.Result, error) {
	namedArgs := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		namedArgs[i] = driver.NamedValue{Value: arg}
	}
	return s.conn.ExecContext(context.Background(), s.query, namedArgs)
}

// Query executes a query that may return rows
func (s *Stmt) Query(args []driver.Value) (driver.Rows, error) {
	namedArgs := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		namedArgs[i] = driver.NamedValue{Value: arg}
	}
	return s.conn.QueryContext(context.Background(), s.query, namedArgs)
}

// Result implements the database/sql/driver.Result interface
type Result struct{}

// LastInsertId returns the database's auto-generated ID after an INSERT
func (r *Result) LastInsertId() (int64, error) {
	return 0, fmt.Errorf("dosqlite: LastInsertId not supported")
}

// RowsAffected returns the number of rows affected by the query
func (r *Result) RowsAffected() (int64, error) {
	return 0, fmt.Errorf("dosqlite: RowsAffected not supported")
}

// Rows implements the database/sql/driver.Rows interface
type Rows struct {
	rows    []map[string]interface{}
	current int
	columns []string
}

// Columns returns the names of the columns
func (r *Rows) Columns() []string {
	if len(r.columns) == 0 && len(r.rows) > 0 {
		for col := range r.rows[0] {
			r.columns = append(r.columns, col)
		}
	}
	return r.columns
}

// Close closes the rows iterator
func (r *Rows) Close() error {
	return nil
}

// Next is called to populate the next row of data into the provided slice
func (r *Rows) Next(dest []driver.Value) error {
	r.current++
	if r.current >= len(r.rows) {
		return io.EOF
	}

	row := r.rows[r.current]
	columns := r.Columns()

	for i, col := range columns {
		if i < len(dest) {
			dest[i] = convertJSONValueToDriver(row[col])
		}
	}

	return nil
}
