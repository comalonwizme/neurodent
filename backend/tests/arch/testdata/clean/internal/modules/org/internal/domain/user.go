// Фикстура теста архитектуры: не компилируется, только парсится.
package domain

import (
	"errors"
	"example.com/fx/internal/shared/id"
	"time"
)

var _ = errors.New
var _ time.Duration
var _ id.ID
