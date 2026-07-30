package migrations

import (
	"embed"
	"sort"
	"strconv"
	"strings"
)

// FS embeds all .sql migration files.
//
//go:embed *.sql
var FS embed.FS

// Sorted returns all embedded migrations sorted by version number.
func Sorted() []Migration {
	entries, err := FS.ReadDir(".")
	if err != nil {
		return nil
	}
	var ms []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".sql")
		parts := strings.SplitN(base, "_", 2)
		if len(parts) != 2 {
			continue
		}
		v, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		data, err := FS.ReadFile(e.Name())
		if err != nil {
			continue
		}
		ms = append(ms, Migration{Version: v, SQL: string(data)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Version < ms[j].Version })
	return ms
}

// Migration holds a versioned SQL schema change.
type Migration struct {
	Version int
	SQL     string
}
