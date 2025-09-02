package dosqlite

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

type WorkerServer struct {
	cmd       *exec.Cmd
	URL       string
	port      int
	stderrBuf *bytes.Buffer

	ctx    context.Context
	cancel context.CancelCauseFunc
}

func getAvailablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("Failed to get available port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

type Opt func(cmd *WorkerServer)

func NewWorkerServer(t *testing.T, opts ...Opt) *WorkerServer {
	t.Helper()
	port := getAvailablePort(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	ws := &WorkerServer{
		URL:       fmt.Sprintf("http://localhost:%d", port),
		port:      port,
		stderrBuf: &bytes.Buffer{},
		ctx:       ctx,
		cancel:    cancel,
	}

	t.Cleanup(func() {
		_ = os.RemoveAll("./test/.mf")
		ws.Close()
	})

	ws.cmd = exec.Command("deno", "task", "start")
	ws.cmd.Dir = filepath.Join(".", "test")
	ws.cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port))
	for _, opt := range opts {
		opt(ws)
	}
	// Someone will explain to be one day why I can't just assign the buffer to
	// cmd.Std(out|err)
	stdoutPipe, _ := ws.cmd.StdoutPipe()
	stderrPipe, _ := ws.cmd.StderrPipe()
	go func() { _, _ = io.Copy(ws.stderrBuf, stderrPipe) }()
	go func() { _, _ = io.Copy(ws.stderrBuf, stdoutPipe) }()
	if err := ws.cmd.Start(); err != nil {
		t.Fatalf("failed to start worker: %v", err)
	}

	go func() {
		ws.cancel(ws.cmd.Wait())
	}()

	if err := ws.waitForWorker(); err != nil {
		t.Fatalf("Failed to wait for worker: %v", err)
	}

	return ws
}

func (ws *WorkerServer) waitForWorker() error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for {
		select {
		case <-ws.ctx.Done():
			if err := context.Cause(ws.ctx); err != nil {
				return fmt.Errorf("worker process exited early:\n%s \n%s", err, ws.stderrBuf.String())
			}
			return fmt.Errorf("worker process exited unexpectedly")
		case <-ctx.Done():
			return fmt.Errorf("worker failed to start after 5 seconds:\n%s", ws.stderrBuf.String())
		case <-ticker.C:
			conn, err := net.Dial("tcp", fmt.Sprintf("localhost:%d", ws.port))
			if err == nil {
				conn.Close()
				return nil
			}
		}
	}
}

func (ws *WorkerServer) Close() {
	if ws.cmd != nil && ws.cmd.Process != nil {
		_ = ws.cmd.Process.Kill()
		<-ws.ctx.Done()
	}
}

func TestTCPConnection(t *testing.T) {
	goPort := getAvailablePort(t)
	server := NewWorkerServer(t, func(ws *WorkerServer) {
		ws.cmd.Env = append(ws.cmd.Env, fmt.Sprintf("GO_PORT=%d", goPort))
	})

	listener, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", goPort))
	if err != nil {
		t.Fatalf("Failed to listen on port %d: %v", goPort, err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("hello\n"))
			_, _ = io.Copy(os.Stdout, conn)
		}
	}()

	res, err := http.Get(server.URL)
	if err != nil {
		t.Fatalf("Failed to get worker: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("Failed to read body: %v", err)
	}
	fmt.Println(string(body))
}

func TestDriver(t *testing.T) {
	goPort := getAvailablePort(t)
	fmt.Println("goPort", goPort)
	server := NewWorkerServer(t, func(ws *WorkerServer) {
		ws.cmd.Env = append(ws.cmd.Env, fmt.Sprintf("GO_PORT=%d", goPort))
	})
	errg, _ := errgroup.WithContext(context.Background())
	var db *sql.DB
	errg.Go(func() error {
		var err error
		db, err = sql.Open("dosqlite", fmt.Sprintf("dosqlite://127.0.0.1:%d", goPort))
		if err != nil {
			return err
		}
		// Must ping to get a connection started.
		return db.Ping()
	})
	errg.Go(func() error {
		// HTTP request to ensure we have a tcp connection
		if _, err := http.Get(server.URL); err != nil {
			return err
		}
		return nil
	})
	if err := errg.Wait(); err != nil {
		t.Fatalf("Failed to wait for worker: %v", err)
	}

	// Helper functions for cleaner test definitions
	expectNoError := func(t *testing.T, rows *sql.Rows, err error) {
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
	}

	expectError := func(t *testing.T, rows *sql.Rows, err error) {
		if err == nil {
			t.Error("Expected error but got none")
		}
	}

	expectUsers := func(expected []map[string]interface{}) func(*testing.T, *sql.Rows, error) {
		return func(t *testing.T, rows *sql.Rows, err error) {
			if err != nil {
				t.Errorf("Query failed: %v", err)
				return
			}
			defer rows.Close()

			var results []map[string]interface{}
			for rows.Next() {
				row := scanRow(t, rows)
				if row != nil {
					results = append(results, row)
				}
			}

			if len(results) != len(expected) {
				t.Errorf("Expected %d rows, got %d", len(expected), len(results))
				return
			}

			for i, expectedRow := range expected {
				if i < len(results) {
					compareRow(t, i, expectedRow, results[i])
				}
			}
		}
	}

	tests := []struct {
		name     string
		query    string
		args     []interface{}
		validate func(t *testing.T, rows *sql.Rows, err error)
	}{
		{
			name:     "create_table",
			query:    "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, age INTEGER, active BOOLEAN, balance REAL, avatar BLOB)",
			validate: expectNoError,
		},
		{
			name:     "insert_text_and_numbers",
			query:    "INSERT INTO users (name, age, active, balance) VALUES (?, ?, ?, ?)",
			args:     []interface{}{"Alice", 25, true, 1000.50},
			validate: expectNoError,
		},
		{
			name:     "insert_with_blob",
			query:    "INSERT INTO users (name, age, avatar) VALUES (?, ?, ?)",
			args:     []interface{}{"Bob", 30, []byte{0x89, 0x50, 0x4E, 0x47}},
			validate: expectNoError,
		},
		{
			name:     "insert_null_values",
			query:    "INSERT INTO users (name, age, active, balance, avatar) VALUES (?, ?, ?, ?, ?)",
			args:     []interface{}{"Charlie", nil, nil, nil, nil},
			validate: expectNoError,
		},
		{
			name:  "select_all_users",
			query: "SELECT id, name, age, active, balance, avatar FROM users ORDER BY id",
			validate: expectUsers([]map[string]interface{}{
				{"id": 1, "name": "Alice", "age": 25, "active": true, "balance": 1000.50, "avatar": nil},
				{"id": 2, "name": "Bob", "age": 30, "active": nil, "balance": nil, "avatar": []byte{0x89, 0x50, 0x4E, 0x47}},
				{"id": 3, "name": "Charlie", "age": nil, "active": nil, "balance": nil, "avatar": nil},
			}),
		},
		{
			name:  "select_with_where_clause",
			query: "SELECT name, age FROM users WHERE age > ? AND active = ? ORDER BY name",
			args:  []interface{}{20, true},
			validate: expectUsers([]map[string]interface{}{
				{"name": "Alice", "age": 25},
			}),
		},
		{
			name:     "update_users",
			query:    "UPDATE users SET age = ? WHERE name = ?",
			args:     []interface{}{26, "Alice"},
			validate: expectNoError,
		},
		{
			name:  "verify_update",
			query: "SELECT age FROM users WHERE name = ?",
			args:  []interface{}{"Alice"},
			validate: expectUsers([]map[string]interface{}{
				{"age": 26},
			}),
		},
		{
			name:     "delete_user",
			query:    "DELETE FROM users WHERE name = ?",
			args:     []interface{}{"Charlie"},
			validate: expectNoError,
		},
		{
			name:  "count_after_delete",
			query: "SELECT COUNT(*) as count FROM users",
			validate: expectUsers([]map[string]interface{}{
				{"count": 2},
			}),
		},
		{
			name:     "error_handling_bad_sql",
			query:    "SELECT * FROM nonexistent_table",
			validate: expectError,
		},
		{
			name:  "prepared_statement_test",
			query: "SELECT name FROM users WHERE age = ?",
			args:  []interface{}{26},
			validate: expectUsers([]map[string]interface{}{
				{"name": "Alice"},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Execute the query
			if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(tt.query)), "SELECT") {
				rows, err := db.Query(tt.query, tt.args...)
				tt.validate(t, rows, err)
			} else {
				_, err := db.Exec(tt.query, tt.args...)
				tt.validate(t, nil, err)
			}
		})
	}
}

// scanRow is a helper function to scan a row into a map
func scanRow(t *testing.T, rows *sql.Rows) map[string]interface{} {
	columns, err := rows.Columns()
	if err != nil {
		t.Errorf("Failed to get columns: %v", err)
		return nil
	}

	values := make([]interface{}, len(columns))
	valuePtrs := make([]interface{}, len(columns))
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	err = rows.Scan(valuePtrs...)
	if err != nil {
		t.Errorf("Failed to scan row: %v", err)
		return nil
	}

	result := make(map[string]interface{})
	for i, col := range columns {
		val := values[i]
		// Handle type conversions
		if val != nil {
			switch v := val.(type) {
			case int64:
				result[col] = int(v)
			case string:
				// Handle boolean strings
				if col == "active" && (v == "true" || v == "false") {
					result[col] = v == "true"
				} else {
					result[col] = v
				}
			default:
				result[col] = val
			}
		} else {
			result[col] = nil
		}
	}
	return result
}

// compareRow compares expected vs actual row data
func compareRow(t *testing.T, rowIndex int, expected, actual map[string]interface{}) {
	for key, expectedVal := range expected {
		actualVal, exists := actual[key]
		if !exists {
			t.Errorf("Row %d: missing column %s", rowIndex, key)
			continue
		}

		// Special handling for byte slices
		if expectedBytes, ok := expectedVal.([]byte); ok {
			if actualBytes, ok := actualVal.([]byte); ok {
				if !bytes.Equal(expectedBytes, actualBytes) {
					t.Errorf("Row %d, column %s: expected %v, got %v", rowIndex, key, expectedVal, actualVal)
				}
			} else {
				t.Errorf("Row %d, column %s: expected []byte, got %T", rowIndex, key, actualVal)
			}
		} else if expectedVal != actualVal {
			t.Errorf("Row %d, column %s: expected %v, got %v", rowIndex, key, expectedVal, actualVal)
		}
	}
}
