// Фикстура теста архитектуры: не компилируется, только парсится.
package domain

import (
	"net/http"
)

var _ http.Handler
