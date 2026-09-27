// Фикстура теста архитектуры: не компилируется, только парсится.
package ports

import (
	"context"
	"example.com/fx/internal/modules/iam/internal/domain"
)

var _ context.Context
var _ domain.X
