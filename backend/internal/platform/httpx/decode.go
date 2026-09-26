package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"

	"github.com/comalonwizme/neurodent/backend/internal/shared/apperr"
)

// DecodeJSON строго читает тело запроса в dst (указатель на структуру).
//
// Строгость здесь — часть безопасности, а не педантизм:
//   - Content-Type обязан быть application/json. С cookie-сессиями это защита
//     от CSRF: HTML-форма не может отправить application/json без CORS-preflight;
//   - неизвестные поля — ошибка. Опечатка клиента ("patient_idd") иначе
//     молча превращается в нулевое значение, а попытка передать поле, которого
//     нет в DTO ("clinic_id"), — в тихо проигнорированную атаку;
//   - ровно один JSON-объект: без хвоста после него и без null/массивов;
//   - тело не больше maxBytes (http.MaxBytesReader: соединение закроется,
//     и клиент не сможет продолжать слать мегабайты).
//
// Все ошибки клиента — apperr.Invalid с сообщением, которое можно показать:
// в нём имена полей и смещения, но не значения из тела.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if err := checkContentType(r.Header.Get("Content-Type")); err != nil {
		return err
	}

	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return apperr.Wrap(apperr.Invalid, fmt.Sprintf("request body must not exceed %d bytes", maxBytes), err)
		}
		return apperr.Wrap(apperr.Invalid, "cannot read request body", err)
	}

	// Проверка первого байта нужна, потому что Decode молча принимает null
	// (dst не меняется) и отдаёт невнятную ошибку типа на массив или строку.
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 {
		return apperr.New(apperr.Invalid, "request body must not be empty")
	}
	if trimmed[0] != '{' {
		return apperr.New(apperr.Invalid, "request body must be a JSON object")
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return apperr.Wrap(apperr.Invalid, "request body must contain a single JSON object", err)
	}
	return nil
}

func checkContentType(v string) error {
	if v == "" {
		return apperr.New(apperr.Invalid, "Content-Type must be application/json")
	}
	mt, _, err := mime.ParseMediaType(v)
	if err != nil || mt != contentTypeJSON {
		return apperr.Wrap(apperr.Invalid, "Content-Type must be application/json", err)
	}
	return nil
}

// decodeError превращает ошибку encoding/json в сообщение для клиента.
func decodeError(err error) error {
	if se, ok := errors.AsType[*json.SyntaxError](err); ok {
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("malformed JSON at byte offset %d", se.Offset), err)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return apperr.Wrap(apperr.Invalid, "malformed JSON: unexpected end of input", err)
	}
	if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		// te.Type — Go-тип (int64, time.Time): клиенту он ничего не говорит
		// и раскрывает внутреннее устройство. Отдаём тип в терминах JSON.
		if te.Field != "" {
			return apperr.Wrap(apperr.Invalid, fmt.Sprintf("field %q must be %s", te.Field, jsonKind(te.Type)), err)
		}
		return apperr.Wrap(apperr.Invalid, fmt.Sprintf("invalid value at byte offset %d", te.Offset), err)
	}
	// encoding/json не даёт типизированной ошибки для неизвестного поля;
	// формат сообщения стабилен с Go 1.10 и покрыт тестом.
	if field, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		return apperr.Wrap(apperr.Invalid, "unknown field "+field, err)
	}
	if _, ok := errors.AsType[*json.InvalidUnmarshalError](err); ok {
		// dst — не указатель: ошибка программиста, а не клиента.
		return apperr.Wrap(apperr.Internal, "", err)
	}
	return apperr.Wrap(apperr.Invalid, "malformed JSON", err)
}

func jsonKind(t reflect.Type) string {
	if t == nil {
		return "a valid value"
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	case reflect.Map, reflect.Struct:
		return "an object"
	default:
		return "a valid value"
	}
}
