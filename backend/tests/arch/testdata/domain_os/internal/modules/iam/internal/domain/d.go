// Фикстура теста архитектуры: не компилируется, только парсится.
package domain

import (
	"os"
)

var _ = os.Getenv
