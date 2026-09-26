// Фикстура теста архитектуры: не компилируется, только парсится.
package app

import (
	"example.com/fx/internal/modules/iam"
	"example.com/fx/internal/modules/org"
)

var _ = iam.X
var _ = org.X
