// Фикстура теста архитектуры: не компилируется, только парсится.
package app

import (
	t "time"
)

func f(x t.Time) t.Duration { return t.Since(x) }
