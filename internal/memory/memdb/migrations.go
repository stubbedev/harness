// Package memdb is the sqlc-generated access layer of the machine-wide
// memory store, with the schema migrations that build it. The store is
// its own database, not a workspace one, so its migrations are a set of
// their own.
package memdb

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the store's goose migrations, rooted so the files
// sit at the top level.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}
