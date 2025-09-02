package dosqlite

import "errors"

// Error types for the DoSQLite driver
var (
	ErrInvalidFrame   = errors.New("dosqlite: invalid frame format")
	ErrConnectionLost = errors.New("dosqlite: connection lost")
	ErrInvalidJSON    = errors.New("dosqlite: invalid JSON message")
)