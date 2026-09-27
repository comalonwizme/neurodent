// Фикстура теста архитектуры: не компилируется, только парсится.
package org

import (
	"example.com/fx/internal/modules/org/internal/adapters/postgres"
	"example.com/fx/internal/modules/org/internal/app"
)

var _ = app.X
var _ = postgres.X
