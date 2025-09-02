package dosqlite

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
)

func init() {
	sql.Register("dosqlite", &Driver{})
}

// Driver implements the database/sql/driver.Driver interface
type Driver struct{}

var _ driver.Driver = &Driver{}

// Open opens a new connection to the DoSQLite database
func (d *Driver) Open(name string) (driver.Conn, error) {
	u, err := url.Parse(name)
	if err != nil {
		return nil, fmt.Errorf("dosqlite: invalid connection string: %w", err)
	}

	if u.Scheme != "dosqlite" {
		return nil, fmt.Errorf("dosqlite: invalid scheme, expected 'dosqlite'")
	}
	listener := AddListener(u.Host)
	if err := listener.Ready(); err != nil {
		return nil, fmt.Errorf("dosqlite: failed to connect to worker: %w", err)
	}

	return &Conn{
		listener: listener,
	}, nil
}
