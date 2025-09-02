# DoSQLite Driver Specification

## 1. Overview

DoSQLite is a Go `database/sql` driver that provides access to Cloudflare
Durable Object SQLite databases over TCP using a simple JSON protocol with
length prefixing for framing.

**Connection String Format:** `dosqlite://`

**Package:** `github.com/maxmcd/dosqlite`

**Key Features:**

- No transactions or prepared statements (simple pass-through)
- Length-prefixed JSON for easy debugging and implementation
- Proper binary data handling via base64 encoding
- Full SQLite data type support

## 2. Protocol Specification

### 2.1 Transport Layer

**Frame Format:**

```
┌─────────────────┬─────────────────────┐
│   Length (4B)   │    JSON Payload     │
│   Big Endian    │                     │
└─────────────────┴─────────────────────┘
```

- All messages are framed with a 4-byte big-endian length prefix
- Length indicates the size of the following JSON payload in bytes
- No maximum frame size limit
- JSON payload is UTF-8 encoded

### 2.2 Binary Data Handling

Binary data (BLOBs) are encoded as base64 strings in JSON with a special marker:

```json
{
    "type": "blob",
    "data": "SGVsbG8gV29ybGQ="
}
```

This allows JSON parsing while preserving exact binary data.

### 2.3 Message Formats

#### 2.3.1 EXEC Command

**Purpose:** Execute INSERT, UPDATE, DELETE, CREATE, etc.

**Request:**

```json
{
    "cmd": "exec",
    "sql": "INSERT INTO users (name, age, avatar) VALUES (?, ?, ?)",
    "params": [
        "Alice",
        25,
        { "type": "blob", "data": "iVBORw0KGgoAAAANSUhEUgAA..." }
    ]
}
```

**Response (Success):**

```json
{
    "ok": true
}
```

**Response (Error):**

```json
{
    "ok": false,
    "error": "UNIQUE constraint failed: users.email"
}
```

#### 2.3.2 QUERY Command

**Purpose:** Execute SELECT statements that return result sets.

**Request:**

```json
{
    "cmd": "query",
    "sql": "SELECT id, name, age, avatar FROM users WHERE age > ?",
    "params": [21]
}
```

**Response (Success):**

```json
{
    "ok": true,
    "rows": [
        {
            "id": 1,
            "name": "Alice",
            "age": 25,
            "avatar": { "type": "blob", "data": "iVBORw0KGgoAAAANSUhEUgAA..." }
        },
        {
            "id": 2,
            "name": "Bob",
            "age": 30,
            "avatar": null
        }
    ]
}
```

**Response (Error):**

```json
{
    "ok": false,
    "error": "no such table: users"
}
```

## 3. Go Implementation Specification

### 3.1 Package Structure

```
dosqlite/
├── driver.go      // Driver registration and connection opening
├── conn.go        // Connection interface implementation
├── protocol.go    // JSON protocol reader/writer with framing
├── types.go       // Message types and binary data handling
├── errors.go      // Error handling and types
└── driver_test.go // Test suite
```

### 3.2 Core Types

#### 3.2.1 Message Types

```go
// Command types
type ExecRequest struct {
    Cmd    string        `json:"cmd"`    // "exec"
    SQL    string        `json:"sql"`
    Params []interface{} `json:"params"`
}

type ExecResponse struct {
    OK    bool   `json:"ok"`
    Error string `json:"error,omitempty"`
}

type QueryRequest struct {
    Cmd    string        `json:"cmd"`    // "query"
    SQL    string        `json:"sql"`
    Params []interface{} `json:"params"`
}

type QueryResponse struct {
    OK    bool                     `json:"ok"`
    Rows  []map[string]interface{} `json:"rows,omitempty"`
    Error string                   `json:"error,omitempty"`
}

// Binary data wrapper
type BlobValue struct {
    Type string `json:"type"` // "blob"
    Data string `json:"data"` // base64 encoded
}
```

#### 3.2.2 Protocol Reader/Writer

```go
type ProtocolConn struct {
    conn net.Conn
    mu   sync.Mutex
}

// Error types
var (
    ErrInvalidFrame   = errors.New("dosqlite: invalid frame format")
    ErrConnectionLost = errors.New("dosqlite: connection lost")
    ErrInvalidJSON    = errors.New("dosqlite: invalid JSON message")
)
```

### 3.3 Driver Interface Implementation

#### 3.3.1 Driver Registration

```go
package dosqlite

import (
    "context"
    "database/sql"
    "database/sql/driver"
    "encoding/json"
    "fmt"
    "net"
    "net/url"
)

func init() {
    sql.Register("dosqlite", &Driver{})
}

type Driver struct{}

func (d *Driver) Open(name string) (driver.Conn, error) {
    u, err := url.Parse(name)
    if err != nil {
        return nil, fmt.Errorf("dosqlite: invalid connection string: %w", err)
    }

    if u.Scheme != "dosqlite" {
        return nil, fmt.Errorf("dosqlite: invalid scheme, expected 'dosqlite'")
    }

    // Connect to localhost:8787 by default
    tcpConn, err := net.Dial("tcp", "localhost:8787")
    if err != nil {
        return nil, fmt.Errorf("dosqlite: connection failed: %w", err)
    }

    return &Conn{
        conn: tcpConn,
    }, nil
}
```

#### 3.3.2 Connection Interface

```go
type Conn struct {
    conn   net.Conn
    mu     sync.Mutex
    closed bool
}

func (c *Conn) Prepare(query string) (driver.Stmt, error) {
    return &Stmt{
        conn:  c,
        query: query,
    }, nil
}

func (c *Conn) Close() error {
    if c.closed {
        return nil
    }
    c.closed = true
    return c.conn.Close()
}

func (c *Conn) Begin() (driver.Tx, error) {
    return nil, fmt.Errorf("dosqlite: transactions not supported")
}

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
```

### 3.4 Statement Interface Implementation

```go
type Stmt struct {
    conn  *Conn
    query string
}

func (s *Stmt) Close() error {
    return nil
}

func (s *Stmt) NumInput() int {
    return strings.Count(s.query, "?")
}

func (s *Stmt) Exec(args []driver.Value) (driver.Result, error) {
    namedArgs := make([]driver.NamedValue, len(args))
    for i, arg := range args {
        namedArgs[i] = driver.NamedValue{Value: arg}
    }
    return s.conn.ExecContext(context.Background(), s.query, namedArgs)
}

func (s *Stmt) Query(args []driver.Value) (driver.Rows, error) {
    namedArgs := make([]driver.NamedValue, len(args))
    for i, arg := range args {
        namedArgs[i] = driver.NamedValue{Value: arg}
    }
    return s.conn.QueryContext(context.Background(), s.query, namedArgs)
}
```

### 3.5 Rows Interface Implementation

```go
type Rows struct {
    rows    []map[string]interface{}
    current int
    columns []string
}

func (r *Rows) Columns() []string {
    if len(r.columns) == 0 && len(r.rows) > 0 {
        for col := range r.rows[0] {
            r.columns = append(r.columns, col)
        }
    }
    return r.columns
}

func (r *Rows) Close() error {
    return nil
}

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
```

### 3.6 Protocol Implementation

```go
func (c *Conn) sendRequest(req interface{}, resp interface{}) error {
    c.mu.Lock()
    defer c.mu.Unlock()

    // Marshal request to JSON
    payload, err := json.Marshal(req)
    if err != nil {
        return fmt.Errorf("dosqlite: failed to marshal request: %w", err)
    }

    // Write frame header (4-byte big-endian length)
    header := make([]byte, 4)
    binary.BigEndian.PutUint32(header, uint32(len(payload)))

    if _, err := c.conn.Write(header); err != nil {
        return driver.ErrBadConn
    }

    // Write JSON payload
    if _, err := c.conn.Write(payload); err != nil {
        return driver.ErrBadConn
    }

    // Read response frame header
    if _, err := io.ReadFull(c.conn, header); err != nil {
        return driver.ErrBadConn
    }

    length := binary.BigEndian.Uint32(header)

    // Read response payload
    responsePayload := make([]byte, length)
    if _, err := io.ReadFull(c.conn, responsePayload); err != nil {
        return driver.ErrBadConn
    }

    // Unmarshal response
    if err := json.Unmarshal(responsePayload, resp); err != nil {
        return fmt.Errorf("dosqlite: failed to unmarshal response: %w", err)
    }

    return nil
}
```

### 3.7 Binary Data and Value Conversion

```go
import (
    "encoding/base64"
    "time"
)

// convertDriverValueToJSON converts database/sql driver values to JSON-safe values
func convertDriverValueToJSON(v driver.Value) interface{} {
    switch val := v.(type) {
    case nil:
        return nil
    case int64:
        return val
    case float64:
        return val
    case string:
        return val
    case []byte:
        if val == nil {
            return nil
        }
        // Encode binary data as base64 with type marker
        return BlobValue{
            Type: "blob",
            Data: base64.StdEncoding.EncodeToString(val),
        }
    case bool:
        return val // JSON supports booleans natively
    case time.Time:
        return val.Unix() // Convert to Unix timestamp
    default:
        return fmt.Sprintf("%v", val) // Fallback to string
    }
}

// convertJSONValueToDriver converts JSON values back to database/sql driver values
func convertJSONValueToDriver(v interface{}) driver.Value {
    switch val := v.(type) {
    case nil:
        return nil
    case float64:
        // JSON numbers are always float64, convert integers back
        if val == float64(int64(val)) {
            return int64(val)
        }
        return val
    case string:
        return val
    case bool:
        return val
    case map[string]interface{}:
        // Check if it's a blob value
        if typeVal, ok := val["type"].(string); ok && typeVal == "blob" {
            if dataVal, ok := val["data"].(string); ok {
                decoded, err := base64.StdEncoding.DecodeString(dataVal)
                if err != nil {
                    return nil // Invalid base64, return null
                }
                return decoded
            }
        }
        return fmt.Sprintf("%v", val) // Fallback to string for unknown objects
    default:
        return fmt.Sprintf("%v", val)
    }
}
```

## 4. TypeScript Worker Implementation Specification

### 4.1 Message Types

```typescript
/// <reference path="../worker-configuration.d.ts" />
import { connect } from "cloudflare:sockets";

interface ExecRequest {
    cmd: "exec";
    sql: string;
    params: any[];
}

interface ExecResponse {
    ok: boolean;
    error?: string;
}

interface QueryRequest {
    cmd: "query";
    sql: string;
    params: any[];
}

interface QueryResponse {
    ok: boolean;
    rows?: Record<string, any>[];
    error?: string;
}

interface BlobValue {
    type: "blob";
    data: string; // base64 encoded
}
```

### 4.2 Protocol Handler

```typescript
class ProtocolHandler {
    private buffer: Uint8Array = new Uint8Array(0);

    addData(data: Uint8Array): void {
        const newBuffer = new Uint8Array(this.buffer.length + data.length);
        newBuffer.set(this.buffer);
        newBuffer.set(data, this.buffer.length);
        this.buffer = newBuffer;
    }

    readFrame(): Uint8Array | null {
        if (this.buffer.length < 4) {
            return null; // Need more data
        }

        // Read 4-byte big-endian length
        const view = new DataView(
            this.buffer.buffer,
            this.buffer.byteOffset,
            this.buffer.byteLength,
        );
        const frameLength = view.getUint32(0, false); // false = big-endian

        if (this.buffer.length < 4 + frameLength) {
            return null; // Need more data
        }

        const frame = this.buffer.slice(4, 4 + frameLength);
        this.buffer = this.buffer.slice(4 + frameLength);
        return frame;
    }

    writeFrame(data: any): Uint8Array {
        const jsonStr = JSON.stringify(data);
        const payload = new TextEncoder().encode(jsonStr);

        // Create frame with 4-byte big-endian length prefix
        const frame = new Uint8Array(4 + payload.length);
        const view = new DataView(frame.buffer);
        view.setUint32(0, payload.length, false); // false = big-endian
        frame.set(payload, 4);

        return frame;
    }
}
```

### 4.3 SQLDriver Durable Object Update

```typescript
export class SQLDriver {
    ctx: DurableObjectState;
    port: number;
    tcpSocket?: Socket;
    protocolHandler: ProtocolHandler;

    constructor(ctx: DurableObjectState, env: { GO_PORT: string }) {
        this.port = Number(env.GO_PORT);
        this.ctx = ctx;
        this.protocolHandler = new ProtocolHandler();
    }

    async ensureTcpConnection() {
        if (!this.tcpSocket) {
            await this.initTcpConnection();
        }
    }

    async initTcpConnection() {
        this.tcpSocket = connect(`localhost:${this.port}`);

        this.tcpSocket.closed.then(() => {
            console.log("TCP connection closed");
            this.tcpSocket = undefined;
        }).catch((err) => {
            console.error("TCP connection error:", this.serializeError(err));
        });

        await this.tcpSocket.opened;
        console.log("TCP connection established");
        this.startProtocolHandler();
    }

    async startProtocolHandler() {
        try {
            const reader = this.tcpSocket!.readable.getReader();
            const writer = this.tcpSocket!.writable.getWriter();

            while (this.tcpSocket) {
                const { value, done } = await reader.read();
                if (done) break;

                this.protocolHandler.addData(value);

                // Process all complete frames
                let frame: Uint8Array | null;
                while ((frame = this.protocolHandler.readFrame()) !== null) {
                    const response = await this.handleCommand(frame);
                    await writer.write(response);
                }
            }
        } catch (err) {
            console.error("Protocol handler error:", this.serializeError(err));
        }
    }

    async handleCommand(frame: Uint8Array): Promise<Uint8Array> {
        try {
            // Parse JSON command
            const jsonStr = new TextDecoder().decode(frame);
            const command = JSON.parse(jsonStr);

            switch (command.cmd) {
                case "exec":
                    return this.handleExec(command as ExecRequest);
                case "query":
                    return this.handleQuery(command as QueryRequest);
                default:
                    return this.protocolHandler.writeFrame({
                        ok: false,
                        error: `Unknown command: ${command.cmd}`,
                    });
            }
        } catch (error) {
            return this.protocolHandler.writeFrame({
                ok: false,
                error: this.serializeError(error),
            });
        }
    }

    async handleExec(req: ExecRequest): Promise<Uint8Array> {
        try {
            // Convert parameters
            const params = req.params.map((p) => this.convertFromJSONValue(p));

            // Execute SQL
            this.ctx.storage.sql.exec(req.sql, ...params);

            const response: ExecResponse = {
                ok: true,
            };

            return this.protocolHandler.writeFrame(response);
        } catch (error) {
            const response: ExecResponse = {
                ok: false,
                error: this.serializeError(error),
            };
            return this.protocolHandler.writeFrame(response);
        }
    }

    async handleQuery(req: QueryRequest): Promise<Uint8Array> {
        try {
            // Convert parameters
            const params = req.params.map((p) => this.convertFromJSONValue(p));

            // Execute query
            const cursor = this.ctx.storage.sql.exec(req.sql, ...params);

            // Convert rows to JSON-safe format using cursor.toArray()
            const rows = cursor.toArray().map((row) => {
                const convertedRow: Record<string, any> = {};
                for (const [key, value] of Object.entries(row)) {
                    convertedRow[key] = this.convertToJSONValue(value);
                }
                return convertedRow;
            });

            const response: QueryResponse = {
                ok: true,
                rows: rows,
            };

            return this.protocolHandler.writeFrame(response);
        } catch (error) {
            const response: QueryResponse = {
                ok: false,
                error: this.serializeError(error),
            };
            return this.protocolHandler.writeFrame(response);
        }
    }

    private convertFromJSONValue(jsonValue: any): any {
        // Handle blob values
        if (typeof jsonValue === "object" && jsonValue?.type === "blob") {
            // Decode base64 to Uint8Array for SQLite
            const decoder = new TextDecoder("base64");
            return Uint8Array.from(
                atob(jsonValue.data),
                (c) => c.charCodeAt(0),
            );
        }
        return jsonValue; // Pass through other values
    }

    private convertToJSONValue(sqliteValue: any): any {
        // Handle binary data from SQLite
        if (sqliteValue instanceof ArrayBuffer) {
            const uint8Array = new Uint8Array(sqliteValue);
            const base64 = btoa(String.fromCharCode(...uint8Array));
            return { type: "blob", data: base64 };
        }
        if (sqliteValue instanceof Uint8Array) {
            const base64 = btoa(String.fromCharCode(...sqliteValue));
            return { type: "blob", data: base64 };
        }
        return sqliteValue; // Pass through numbers, strings, null, boolean
    }

    async fetch(request: Request): Promise<Response> {
        await this.ensureTcpConnection();
        return new Response("DoSQLite TCP Bridge Ready");
    }

    private serializeError(err: unknown): string {
        if (err instanceof Error) {
            return `${err.name}: ${err.message}${
                err.stack ? "\n" + err.stack : ""
            }`;
        }
        return String(err);
    }
}
```

## 5. Implementation Phases

### Phase 1: Core JSON Protocol

**Deliverables:**

1. `protocol.go` - JSON framing reader/writer
2. `types.go` - Message types and binary data handling
3. `errors.go` - Error types and handling
4. Basic protocol unit tests

**Acceptance Criteria:**

- JSON protocol with length framing works correctly
- Binary data handled via base64 encoding/decoding
- Frame handling works with various message sizes
- Error conditions are handled properly

### Phase 2: Basic Driver Implementation

**Deliverables:**

1. `driver.go` - Driver registration and connection opening
2. `conn.go` - Basic connection with exec/query (no prepared statements)
3. Updated `worker/src/index.ts` with JSON command handlers

**Acceptance Criteria:**

- Can execute simple INSERT/UPDATE/DELETE statements
- Can perform SELECT queries and read results
- All SQLite data types including BLOBs work correctly

## 7. Error Handling Specification

**Network Errors:** Return `driver.ErrBadConn` to trigger reconnection **JSON
Errors:** Return specific error with "dosqlite:" prefix **SQL Errors:** Pass
through original SQLite error message

```go
var (
    ErrConnectionLost = errors.New("dosqlite: connection lost")
    ErrInvalidFrame   = errors.New("dosqlite: invalid frame format")
    ErrInvalidJSON    = errors.New("dosqlite: invalid JSON message")
)
```
