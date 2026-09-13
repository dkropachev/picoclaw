// Package databasevalidation defines the pathless, query-only capability used
// to validate one exact provider-retained database generation.
package databasevalidation

import "github.com/sipeed/picoclaw/pkg/database"

// ScalarKind identifies the only result values an exact validator may receive.
type ScalarKind uint8

const (
	ScalarNoRow ScalarKind = iota
	ScalarNull
	ScalarInt64
	ScalarText
)

// Scalar is one copied SQLite value. Fields not selected by Kind are zero.
type Scalar struct {
	Kind  ScalarKind
	Int64 int64
	Text  string
}

// Generation exposes one StoreID/domain binding and synchronous scalar reads.
// It deliberately has no caller-selected context, path, rows iterator,
// mutation, prepare, raw connection, transaction-control, close, arbitrary
// argument, or caller-controlled Scan surface.
type Generation interface {
	StoreID() database.StoreID
	Domain() string
	ReadScalar(statement string, arguments ...string) (Scalar, error)
}
