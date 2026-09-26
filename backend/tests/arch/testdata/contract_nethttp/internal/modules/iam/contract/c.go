// Фикстура теста архитектуры: не компилируется, только парсится.
package contract

import (
	"net/http"
)

var _ http.Handler
