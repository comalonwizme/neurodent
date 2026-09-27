package httpx

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"

	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
)

// ParamErrorHandler — ErrorHandlerFunc для серверов, сгенерированных
// oapi-codegen (ADR-0013): ошибка разбора параметра пути, query или
// заголовка превращается в 400 RFC 9457.
//
// Сообщение клиенту строится из имени параметра (оно из спецификации), а не
// из err.Error(): текст runtime-библиотеки цитирует присланное значение, а
// значения из запроса клиенту не возвращаются (как в DecodeJSON). Полная
// ошибка уходит в лог на уровне debug.
//
// Каждый сгенерированный пакет объявляет свои типы ошибок
// (InvalidParamFormatError, RequiredParamError, …) с полем ParamName, без
// общего интерфейса. Поэтому имя достаётся рефлексией по полю, одинаковому
// во всех пакетах, а не type switch на типы одного пакета.
func ParamErrorHandler(log *slog.Logger) func(w http.ResponseWriter, r *http.Request, err error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		WriteError(w, r, log, apperr.Wrap(apperr.Invalid, paramMessage(err), err))
	}
}

func paramMessage(err error) string {
	for e := err; e != nil; e = errors.Unwrap(e) {
		v := reflect.ValueOf(e)
		if v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		if v.Kind() != reflect.Struct {
			continue
		}
		f := v.FieldByName("ParamName")
		if !f.IsValid() || f.Kind() != reflect.String || f.String() == "" {
			continue
		}
		if v.Type().Name() == "RequiredParamError" || v.Type().Name() == "RequiredHeaderError" {
			return fmt.Sprintf("parameter %q is required", f.String())
		}
		return fmt.Sprintf("parameter %q is invalid", f.String())
	}
	return "request parameters are invalid"
}
