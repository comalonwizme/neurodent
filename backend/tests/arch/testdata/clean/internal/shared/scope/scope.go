// Фикстура теста архитектуры: не компилируется, только парсится.
package scope

import (
	"errors"
	"example.com/fx/internal/shared/id"
)

var _ = errors.New
var _ id.ID
