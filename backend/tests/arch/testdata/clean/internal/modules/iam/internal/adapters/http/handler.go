// Фикстура теста архитектуры: не компилируется, только парсится.
package http

import (
	"example.com/fx/internal/modules/iam/internal/app"
	"example.com/fx/internal/platform/httpx"
	"net/http"
)

var _ http.Handler
var _ = httpx.WriteJSON
var _ app.X
