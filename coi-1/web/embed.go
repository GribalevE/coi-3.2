// Package web встраивает статические файлы UI в бинарник.
//
// Файлы (index.html, static/… и т.п.) лежат рядом с этим файлом,
// поэтому embed-директива видит их без всяких "../".
package web

import "embed"

// FS содержит все файлы из каталога web/ (включая подкаталоги).
// Его передаём в api.Config.WebFS как обычный fs.FS.
//
//go:embed *
var FS embed.FS
