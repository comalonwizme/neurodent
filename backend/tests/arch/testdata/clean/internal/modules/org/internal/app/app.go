// Фикстура теста архитектуры: не компилируется, только парсится.
package app

import (
	"context"
	"example.com/fx/internal/modules/iam/contract"
	"example.com/fx/internal/modules/org/internal/domain"
	"example.com/fx/internal/modules/org/internal/ports"
	"example.com/fx/internal/shared/clock"
)

var _ context.Context
var _ domain.X
var _ ports.X
var _ clock.Clock
var _ contract.X
