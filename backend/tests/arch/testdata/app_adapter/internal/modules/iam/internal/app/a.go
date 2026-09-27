// Фикстура теста архитектуры: не компилируется, только парсится.
package app

import (
	"example.com/fx/internal/modules/iam/internal/adapters/postgres"
)

var _ postgres.X
