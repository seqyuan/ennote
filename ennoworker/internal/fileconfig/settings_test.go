package fileconfig_test

import (
	"path/filepath"
	"testing"

	"github.com/seqyuan/ennote/ennoworker/internal/fileconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSettingsDefaultCatalogFullTextIndexOff(t *testing.T) {
	store := &fileconfig.SettingsStore{Path: filepath.Join(t.TempDir(), "settings.json")}
	doc, err := store.Read()
	require.NoError(t, err)
	assert.Equal(t, fileconfig.FullTextOff, doc.CatalogFullTextIndex)
}

func TestSettingsSetCatalogFullTextIndex(t *testing.T) {
	store := &fileconfig.SettingsStore{Path: filepath.Join(t.TempDir(), "settings.json")}
	require.NoError(t, store.SetCatalogFullTextIndex(fileconfig.FullTextOnDemand))
	doc, err := store.Read()
	require.NoError(t, err)
	assert.Equal(t, fileconfig.FullTextOnDemand, doc.CatalogFullTextIndex)
}

func TestSettingsRejectsInvalidCatalogFullTextIndex(t *testing.T) {
	store := &fileconfig.SettingsStore{Path: filepath.Join(t.TempDir(), "settings.json")}
	err := store.SetCatalogFullTextIndex("always")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "catalogFullTextIndex")
}
