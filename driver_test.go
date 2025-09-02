package dosqlite

import (
	"database/sql"
	"database/sql/driver"
	"testing"
)

func TestDriverRegistration(t *testing.T) {
	// Test that the driver is properly registered
	drivers := sql.Drivers()
	found := false
	for _, driverName := range drivers {
		if driverName == "dosqlite" {
			found = true
			break
		}
	}
	if !found {
		t.Error("dosqlite driver not registered")
	}
}

func TestConnectionStringParsing(t *testing.T) {
	d := &Driver{}

	// Test invalid scheme
	_, err := d.Open("mysql://localhost")
	if err == nil {
		t.Error("expected error for invalid scheme")
	}

	// Test invalid URL
	_, err = d.Open("://invalid")
	if err == nil {
		t.Error("expected error for invalid URL")
	}
}

func TestValueConversion(t *testing.T) {
	tests := []struct {
		name     string
		input    driver.Value
		expected interface{}
	}{
		{"nil", nil, nil},
		{"int64", int64(42), int64(42)},
		{"float64", float64(3.14), float64(3.14)},
		{"string", "hello", "hello"},
		{"bool", true, true},
		{"empty bytes", []byte{}, BlobValue{Type: "blob", Data: ""}},
		{"bytes", []byte{1, 2, 3}, BlobValue{Type: "blob", Data: "AQID"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertDriverValueToJSON(tt.input)
			if tt.name == "bytes" || tt.name == "empty bytes" {
				blob, ok := result.(BlobValue)
				if !ok {
					t.Errorf("expected BlobValue, got %T", result)
					return
				}
				expected := tt.expected.(BlobValue)
				if blob.Type != expected.Type || blob.Data != expected.Data {
					t.Errorf("got %+v, want %+v", blob, expected)
				}
			} else if result != tt.expected {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestJSONValueConversion(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected driver.Value
	}{
		{"nil", nil, nil},
		{"float64 int", float64(42), int64(42)},
		{"float64 decimal", float64(3.14), float64(3.14)},
		{"string", "hello", "hello"},
		{"bool", true, true},
		{"blob", map[string]interface{}{"type": "blob", "data": "AQID"}, []byte{1, 2, 3}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertJSONValueToDriver(tt.input)
			if tt.name == "blob" {
				bytes, ok := result.([]byte)
				if !ok {
					t.Errorf("expected []byte, got %T", result)
					return
				}
				expected := tt.expected.([]byte)
				if len(bytes) != len(expected) {
					t.Errorf("got length %d, want %d", len(bytes), len(expected))
					return
				}
				for i, b := range bytes {
					if b != expected[i] {
						t.Errorf("byte %d: got %d, want %d", i, b, expected[i])
					}
				}
			} else if result != tt.expected {
				t.Errorf("got %v (%T), want %v (%T)", result, result, tt.expected, tt.expected)
			}
		})
	}
}
