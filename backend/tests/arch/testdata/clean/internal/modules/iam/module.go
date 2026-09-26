// Фикстура теста архитектуры: не компилируется, только парсится.
package iam

import (
	"example.com/fx/internal/modules/iam/internal/adapters/postgres"
	"example.com/fx/internal/modules/iam/internal/app"
)

var _ = app.X
var _ = postgres.X
