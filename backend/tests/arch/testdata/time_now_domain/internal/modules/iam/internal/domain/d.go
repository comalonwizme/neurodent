// Фикстура теста архитектуры: не компилируется, только парсится.
package domain

import (
	"time"
)

func f() time.Time { return time.Now() }
