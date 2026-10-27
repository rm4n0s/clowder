package store

import (
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rm4n0s/errors"
	"gorm.io/gorm"
)

// sqlStateUniqueViolation is the SQLSTATE code of PostgreSQL for a unique or
// primary key violation.
const sqlStateUniqueViolation = "23505"

// failure is how the database reported an error, as far as the store cares.
type failure int

const (
	// failureOther is any error without a tag of its own.
	failureOther failure = iota
	// failureNotFound is a query for one row that found none.
	failureNotFound
	// failureConflict is a unique or primary key constraint violation.
	failureConflict
)

// classify says which kind of failure err is. It builds no error: the method
// that failed does, with errors.NewErr, so that the route ends there (R4):
//
//	switch classify(err) {
//	case failureNotFound:
//		return errors.NewErr(TagNotFound, err).SetMetadata("op", "getUser")
//	case failureConflict:
//		return errors.NewErr(TagConflict, err).SetMetadata("op", "createUser")
//	default:
//		return errors.NewErr(TagQueryFailed, err).SetMetadata("op", "getUser")
//	}
//
// The unique violation is the one GORM translates (TranslateError) from the
// SQLite codes 2067 (SQLITE_CONSTRAINT_UNIQUE) and 1555
// (SQLITE_CONSTRAINT_PRIMARYKEY), and from the SQLSTATE 23505 of PostgreSQL;
// an error of pgx that carries 23505 and that GORM did not translate counts
// too.
func classify(err error) failure {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return failureNotFound
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return failureConflict
	case errors.As(err, &pgErr) && pgErr.Code == sqlStateUniqueViolation:
		return failureConflict
	default:
		return failureOther
	}
}

const (
	// TagNotFound is the tag of a query for one row that found none. The
	// metadata "op" names the store method.
	TagNotFound = "NotFound"
	// TagConflict is the tag of a write that violates a unique or primary key
	// constraint. The metadata "op" names the store method.
	TagConflict = "Conflict"
	// TagQueryFailed is the tag of any other database error. The metadata
	// "op" names the store method.
	TagQueryFailed = "QueryFailed"
)
