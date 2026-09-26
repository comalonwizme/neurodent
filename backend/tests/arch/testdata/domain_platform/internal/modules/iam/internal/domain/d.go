// Фикстура теста архитектуры: не компилируется, только парсится.
package domain

import (
	"example.com/fx/internal/platform/postgres"
)

var _ postgres.DB
