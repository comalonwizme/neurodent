// Фикстура теста архитектуры: не компилируется, только парсится.
package http

import (
	"github.com/jackc/pgx/v5"
)

var _ pgx.Tx
