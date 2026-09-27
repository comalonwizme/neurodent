// Фикстура теста архитектуры: не компилируется, только парсится.
package http

import (
	"example.com/fx/internal/platform/postgres"
)

var _ postgres.DB
