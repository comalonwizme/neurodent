// Фикстура теста архитектуры: не компилируется, только парсится.
package postgres

import (
	"example.com/fx/internal/modules/iam/internal/domain"
	pg "example.com/fx/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
)

var _ pgx.Tx
var _ pg.DB
var _ domain.X
