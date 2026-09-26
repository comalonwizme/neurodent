// Фикстура теста архитектуры: не компилируется, только парсится.
package contract

import (
	"github.com/jackc/pgx/v5"
)

var _ pgx.Tx
