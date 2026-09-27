// Фикстура теста архитектуры: не компилируется, только парсится.
package contract

import (
	"context"
	"example.com/fx/internal/shared/id"
)

var _ context.Context
var _ id.ID
