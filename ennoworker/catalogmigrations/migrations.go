package catalogmigrations

import "embed"

//go:embed *.sql
var FS embed.FS

func InitialSchema() (string, error) {
	contents, err := FS.ReadFile("0001_catalog.sql")
	return string(contents), err
}
