package artifacts

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceStoresAndLoadsManagedImage(t *testing.T) {
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))
	now := "2026-07-27T00:00:00Z"
	_, err = db.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','project',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sessions(id,project_id,created_at,updated_at) VALUES('s','p',?,?)`, now, now)
	require.NoError(t, err)
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	require.NoError(t, err)
	service := &Service{DB: db, Root: t.TempDir()}
	artifact, err := service.StoreImage(context.Background(), "p", "s", "pixel.png", png)
	require.NoError(t, err)
	assert.Equal(t, "image/png", artifact.MIMEType)
	loaded, err := service.ValidateForSession(context.Background(), artifact.ID, "s")
	require.NoError(t, err)
	assert.Equal(t, png, loaded.Data)
	assert.Equal(t, 1, loaded.Width)
}

func TestServiceRejectsUnsupportedOrCrossProjectImage(t *testing.T) {
	db, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, store.Migrate(db))
	now := "2026-07-27T00:00:00Z"
	_, err = db.Exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','project',?,?)`, now, now)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO sessions(id,project_id,created_at,updated_at) VALUES('s','p',?,?)`, now, now)
	require.NoError(t, err)
	service := &Service{DB: db, Root: t.TempDir()}
	_, err = service.StoreImage(context.Background(), "p", "s", "not.png", []byte("not an image"))
	assert.Error(t, err)
	_, err = service.StoreImage(context.Background(), "other", "s", "not.png", make([]byte, 8))
	assert.Error(t, err)
}
