package store

import (
	"context"

	"gorm.io/gorm"
)

// DB gives the external test package the GORM handle that only this package
// uses otherwise.
func (s *Store) DB(ctx context.Context) *gorm.DB {
	return s.db(ctx)
}
