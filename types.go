package dosqlite

import (
	"database/sql/driver"
	"encoding/base64"
	"fmt"
	"time"
)

// ExecRequest represents a request to execute a non-query SQL statement
type ExecRequest struct {
	Cmd    string        `json:"cmd"`    // "exec"
	SQL    string        `json:"sql"`
	Params []interface{} `json:"params"`
}

// ExecResponse represents the response from executing a non-query SQL statement
type ExecResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// QueryRequest represents a request to execute a query SQL statement
type QueryRequest struct {
	Cmd    string        `json:"cmd"`    // "query"
	SQL    string        `json:"sql"`
	Params []interface{} `json:"params"`
}

// QueryResponse represents the response from executing a query SQL statement
type QueryResponse struct {
	OK    bool                     `json:"ok"`
	Rows  []map[string]interface{} `json:"rows,omitempty"`
	Error string                   `json:"error,omitempty"`
}

// BlobValue represents binary data for JSON encoding
type BlobValue struct {
	Type string `json:"type"` // "blob"
	Data string `json:"data"` // base64 encoded
}

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