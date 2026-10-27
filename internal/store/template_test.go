package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rm4n0s/clowder/internal/clock"
)

func TestTemplate(t *testing.T) {
	ctx := t.Context()
	tmpl, err := NewTemplate(ctx)
	if err != nil {
		t.Fatalf("NewTemplate: %+v", err)
	}

	t.Run("a copy is migrated and independent", func(t *testing.T) {
		open := func(name string) *Store {
			t.Helper()
			s, err := tmpl.Open(ctx, filepath.Join(t.TempDir(), name), clock.NewFake(fakeStart), nil)
			if err != nil {
				t.Fatalf("Template.Open: %+v", err)
			}
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Errorf("close: %v", err)
				}
			})
			return s
		}
		a, b := open("a.db"), open("b.db")
		var migrated int64
		if err := a.db(ctx).Model(&schemaMigration{}).Where("id = ?", "0001_posts_fts").Count(&migrated).Error; err != nil || migrated != 1 {
			t.Errorf("migration 0001_posts_fts recorded %d times, err %v", migrated, err)
		}
		u := &User{Username: "ann", DisplayName: "Ann", PasswordHash: "h", Role: RoleMember}
		if err := a.CreateUser(ctx, u); err != nil {
			t.Fatalf("CreateUser: %+v", err)
		}
		if _, err := a.UserByID(ctx, u.ID); err != nil {
			t.Errorf("the copy that wrote the user lost it: %+v", err)
		}
		_ = requireRoute(t, errOf(b.UserByID(ctx, u.ID)), "UserByID.NotFound")
		// The full-text index works in a copy.
		if hits, err := a.SearchPosts(ctx, SearchQuery{ReaderID: u.ID, Terms: []SearchTerm{{Text: "x"}}, Limit: 1}); err != nil || len(hits) != 0 {
			t.Errorf("SearchPosts = %v, %v", hits, err)
		}
	})

	tests := []struct {
		name  string
		run   func(t *testing.T) error
		route string
	}{
		{
			name: "the file cannot be written",
			run: func(t *testing.T) error {
				_, err := tmpl.Open(ctx, filepath.Join(t.TempDir(), "missing", "x.db"), nil, nil)
				return err
			},
			route: "Template.Open.TemplateWriteFailed",
		},
		{
			name: "the scratch directory cannot be made",
			run: func(t *testing.T) error {
				t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
				_, err := NewTemplate(ctx)
				return err
			},
			route: "NewTemplate.TemplateDirFailed",
		},
		{
			name: "the migrated file cannot be read",
			run: func(t *testing.T) error {
				// A URI opens the real file; reading the same string as a path
				// finds nothing.
				_, err := templateAt(ctx, "file:"+filepath.Join(t.TempDir(), "t.db"))
				return err
			},
			route: "templateAt.TemplateReadFailed",
		},
		{
			name: "the migration fails",
			run: func(*testing.T) error {
				done, cancel := context.WithCancel(ctx)
				cancel()
				_, err := NewTemplate(done)
				return err
			},
			route: "NewTemplate->templateAt->Open",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = requireRoute(t, tt.run(t), tt.route)
		})
	}
}

// errOf returns the error of a call that also returns a value.
func errOf[T any](_ T, err error) error {
	return err
}
