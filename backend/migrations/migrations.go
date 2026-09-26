// Package migrations встраивает SQL-миграции в бинарник cmd/migrate:
// в образ не нужно класть файлы рядом, и версия схемы жёстко связана
// с версией кода.
package migrations

import (
	"embed"
	"io/fs"
)

// files — исключение из правила «без глобальных переменных»: go:embed
// работает только с переменными уровня пакета. Переменная не
// экспортируется, а embed.FS доступен только на чтение.
//
//go:embed *.sql
var files embed.FS

// FS возвращает встроенные миграции (файлы NNNN_name.sql в корне).
func FS() fs.FS { return files }
