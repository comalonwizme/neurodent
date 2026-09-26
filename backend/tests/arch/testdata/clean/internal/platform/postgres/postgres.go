// Фикстура теста архитектуры: не компилируется, только парсится.
package postgres

import (
	"example.com/fx/internal/shared/scope"
	"github.com/jackc/pgx/v5"
)

var _ pgx.Tx
var _ scope.Scope
