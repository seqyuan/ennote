package sessionmigrations

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSortedOrdersMigrationsAndRejectsBadNames(t *testing.T) {
	migrations, err := sorted(fstest.MapFS{
		"0002_second.sql": {Data: []byte("SELECT 2;")},
		"0001_first.sql":  {Data: []byte("SELECT 1;")},
		"notes.txt":       {Data: []byte("ignored")},
	})
	require.NoError(t, err)
	require.Len(t, migrations, 2)
	assert.Equal(t, 1, migrations[0].Version)
	assert.Equal(t, "SELECT 1;", migrations[0].SQL)
	assert.Equal(t, 2, migrations[1].Version)

	_, err = sorted(fstest.MapFS{"oops.sql": {Data: []byte("SELECT 1;")}})
	require.ErrorContains(t, err, "invalid session migration file name")

	_, err = sorted(fstest.MapFS{"0001_one.sql": {Data: []byte("SELECT 1;")}, "0001_two.sql": {Data: []byte("SELECT 2;")}})
	require.ErrorContains(t, err, "duplicate session migration version 1")
}
