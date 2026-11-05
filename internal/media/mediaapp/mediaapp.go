// Package mediaapp runs the media use cases: staff with catalog.manage upload
// product images and anyone may read them, as they read the catalog.
package mediaapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"webee_rentor/internal/audit"
	"webee_rentor/internal/audit/auditpg"
	"webee_rentor/internal/identity"
	"webee_rentor/internal/media"
	"webee_rentor/internal/platform/ids"
)

// Service holds the dependencies of the media use cases.
type Service struct {
	pool     *pgxpool.Pool
	store    media.Store
	maxBytes int64
	now      func() time.Time
	newID    func() string
}

// New builds the service. maxBytes limits one upload.
func New(pool *pgxpool.Pool, store media.Store, maxBytes int64) *Service {
	return &Service{pool: pool, store: store, maxBytes: maxBytes, now: time.Now, newID: ids.New}
}

// Uploaded is a stored image.
type Uploaded struct {
	Name        string
	ContentType string
	Size        int
}

// UploadImage stores an image read from body. The permission is checked first,
// so a body is only read for someone who may upload.
func (s *Service) UploadImage(ctx context.Context, p identity.Principal, body io.Reader) (Uploaded, error) {
	if err := p.Require(identity.PermCatalogManage); err != nil {
		return Uploaded{}, err
	}
	data, err := io.ReadAll(io.LimitReader(body, s.maxBytes+1))
	if err != nil {
		return Uploaded{}, fmt.Errorf("read upload: %w", err)
	}
	if int64(len(data)) > s.maxBytes {
		return Uploaded{}, media.ErrFileTooLarge
	}
	format, err := media.DetectImage(data)
	if err != nil {
		return Uploaded{}, err
	}
	name := media.FileName(s.newID(), format)
	if err := s.store.Put(ctx, name, data); err != nil {
		return Uploaded{}, err
	}
	err = auditpg.Append(ctx, s.pool, audit.Event{
		ID: s.newID(), OccurredAt: s.now().UTC(), Actor: p.Actor(), Action: "media.image_uploaded", ResourceType: "media_file",
		ResourceID: name, Result: audit.ResultSuccess,
	})
	if err != nil {
		// The request may have run out of time; the file must still go.
		return Uploaded{}, errors.Join(err, s.store.Delete(context.WithoutCancel(ctx), name))
	}
	return Uploaded{Name: name, ContentType: format.ContentType, Size: len(data)}, nil
}

// Open reads a stored image. A name that is not one of ours is simply not found.
func (s *Service) Open(ctx context.Context, name string) (media.File, media.Format, error) {
	format, ok := media.FormatOf(name)
	if !ok {
		return media.File{}, media.Format{}, media.ErrNotFound
	}
	f, err := s.store.Open(ctx, name)
	if err != nil {
		return media.File{}, media.Format{}, err
	}
	return f, format, nil
}
