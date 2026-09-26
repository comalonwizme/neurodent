// Фикстура теста архитектуры: не компилируется, только парсится.
package app

import (
	"example.com/fx/internal/platform/postgres"
)

var _ postgres.DB
