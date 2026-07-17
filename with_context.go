package gorm

import (
	"context"
)

const ctxKey = "gorm:context"

// WithContext returns a new DB instance that stores the given context.
// v1 gorm has no native context support in the database/sql layer;
// this stores ctx in db.values so downstream code can retrieve it
// via GetContext() and use it with context-aware driver methods.
//
// Usage:
//
//	db.WithContext(ctx).Find(&users)
func (s *DB) WithContext(ctx context.Context) *DB {
	return s.Set(ctxKey, ctx)
}

// GetContext retrieves the context stored via WithContext, or
// context.Background() if none was set.
func (s *DB) GetContext() context.Context {
	if ctx, ok := s.Get(ctxKey); ok {
		if c, ok := ctx.(context.Context); ok && c != nil {
			return c
		}
	}
	return context.Background()
}
