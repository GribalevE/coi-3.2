// migrations/embed.go

// Package migrations встраивает SQL-файлы схемы в бинарник.
// Благодаря этому не нужно тащить .sql рядом с исполняемым файлом.
package migrations

import _ "embed"

// Initial содержит начальную схему SQLite.
//
//go:embed 0001_init.sql
var Initial string