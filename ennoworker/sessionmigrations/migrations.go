package sessionmigrations

import (
	"embed"
	"sort"
	"strconv"
	"strings"
)

//go:embed *.sql
var FS embed.FS

type Migration struct {
	Version int
	SQL     string
}

func Sorted() []Migration {
	entries, err := FS.ReadDir(".")
	if err != nil {
		return nil
	}
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(strings.TrimSuffix(entry.Name(), ".sql"), "_", 2)
		if len(parts) != 2 {
			continue
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		contents, err := FS.ReadFile(entry.Name())
		if err != nil {
			continue
		}
		migrations = append(migrations, Migration{Version: version, SQL: string(contents)})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations
}
