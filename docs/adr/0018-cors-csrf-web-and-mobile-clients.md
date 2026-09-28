# 0018. CORS и CSRF для двух веб-приложений и мобильных клиентов

- Статус: Принято (после ревью, 2026-09-28)
- Дата: 2026-09-28
- Заменяет: [0015](0015-cors-csrf-and-middleware-order.md)
- Связанные: 0016 (rate limit), 0017 (сессии), 0019 (клиентские приложения)

## Контекст

[0015](0015-cors-csrf-and-middleware-order.md) рассчитан на один
браузерный клиент: `app.<домен>` → `api.<домен>`, один список origin
(`NEURODENT_CORS_ALLOWED_ORIGINS`) для CORS с credentials и для
`CrossOriginProtection`.

По [0019](0019-client-applications-and-platforms.md) клиентов теперь
четыре:

- веб клиники `clinic.<домен>`;
- веб пациента `my.<домен>`;
- мобильные оболочки обоих приложений (Capacitor).

Сессии ([0017](0017-sessions-audiences-transports-handoff.md)): cookie
для веба, bearer для мобильных, cookie привязана к origin своей
аудитории.

Что ломается в 0015:

- **Мобильные оболочки не проходят CORS.** Их origin
  (`capacitor://localhost`, `https://localhost`) не в списке, а preflight
  с `Authorization` не разрешён.
- **CrossOrigin отвергает их небезопасные запросы.** WebView ставит
  `Sec-Fetch-Site: cross-site` и `Origin`, поэтому 0015 не считает их
  «не браузерными» и не пропускает.
- **Один список origin не говорит, какому приложению принадлежит
  origin.** Привязке cookie к аудитории (0017, п. 4) это знание нужно.

## Решение

### Из 0015 в силе остаются

- **Порядок цепочки без изменений:**

  ```
  RequestID → AccessLog → SecurityHeaders → Recover → CORS → CrossOrigin → [Auth] → RateLimit → BodyLimit → Timeout → routeProblems(mux)
  ```

  Место `[Auth]` теперь описано в 0017: выбор cookie по группе маршрутов,
  привязка cookie к origin аудитории, bearer.
- `Vary: Origin` на всех ответах.
- CORS-заголовки на любом ответе разрешённому origin, включая ошибки.
- `Access-Control-Expose-Headers: X-Request-ID, Retry-After`.
- Preflight отвечает middleware: `204` при успехе, `Max-Age: 7200`.
  Чужой origin, метод или заголовок — `403` RFC 9457 без CORS-заголовков.
- Запрет `*`, шаблонов и `null`. Формат `scheme://host[:port]`.
- CrossOrigin — stdlib `http.CrossOriginProtection`, отказ — `403`
  RFC 9457. Запросы без `Sec-Fetch-Site` и `Origin` (сервисы, curl)
  пропускаются.
- Preflight в access-логе помечается `route=preflight`.

### Конфигурация: три списка вместо одного

| Переменная | Что | Вне dev |
|---|---|---|
| `NEURODENT_CLINIC_WEB_ORIGINS` | веб-приложение клиники | обязательна, только `https` |
| `NEURODENT_PATIENT_WEB_ORIGINS` | веб-приложение пациента | обязательна, только `https` |
| `NEURODENT_MOBILE_ORIGINS` | мобильные оболочки | может быть пустой |

`NEURODENT_CORS_ALLOWED_ORIGINS` удаляется. Сломать нечего: окружений,
кроме dev, ещё нет.

Проверки при старте (двухэтапная валидация, 0010):

- **Один origin в двух списках — отказ старта.** Привязка cookie к
  аудитории (0017) стала бы неоднозначной.
- **Мобильный список — только из закрытого набора:**
  `capacitor://localhost`, `https://localhost`. Любое другое значение —
  отказ старта. Исключение — dev: там допускается `http://` для live
  reload оболочки с машины разработчика.
- **`localhost` в веб-списках вне dev — отказ старта.**
- Пустой веб-список в dev — CORS для этой аудитории выключен: Angular
  dev-server проксирует API, запросы same-origin (как в 0015).

### CORS: две политики

**Веб-origin** (объединение двух веб-списков) — как в 0015:
- `Access-Control-Allow-Credentials: true`;
- `Allow-Headers`: `Content-Type, X-Request-ID`.

`Authorization` с веб-origin **не разрешён**. Веб не может пользоваться
bearer, значит bearer-токен никогда не окажется в JS-доступном
хранилище браузера (0017, п. 3).

**Мобильные origin:**
- `Access-Control-Allow-Credentials` **нет**: cookie мобильный клиент не
  использует, браузерный движок WebView их не отправит и не примет;
- `Allow-Headers`: `Content-Type, X-Request-ID, Authorization`;
- `Allow-Methods`, `Expose-Headers`, `Max-Age` — как у веба.

**CORS для мобильных origin не является механизмом защиты.** Эти origin
одинаковы у всех приложений на Capacitor в мире. Любое такое приложение
может обратиться к API, как и curl. Защищает bearer-токен: браузер не
подставляет его сам. CORS здесь только разрешает нашей оболочке прочитать
ответ.

### CrossOrigin: доверенные origin — все три списка

`CrossOriginProtection` получает объединение веб- и мобильного списков.

Почему доверять мобильным origin безопасно. CSRF — это подделка запроса с
**автоматически** приложенными учётными данными.

- Cookie сессий `SameSite=Strict` (0017), а `localhost` и `capacitor://`
  не same-site с `<домен>`. Браузер не приложит cookie к запросу с этих
  origin.
- Bearer браузер не прикладывает никогда.

Запрос с мобильного origin не несёт автоматических учётных данных, и
подделывать в нём нечего.

Привязку cookie к аудитории CrossOrigin не проверяет: он не знает, какую
cookie выберет группа маршрутов. Это делает Auth (0017, п. 4).

### Отличия от 0015

- Три списка origin вместо одного, с аудиторией у веб-origin.
- Вторая CORS-политика для мобильных: без credentials, с
  `Authorization`.
- `Authorization` запрещён с веб-origin.
- Мобильные origin — закрытый набор значений, проверяемый при старте.
- Мобильные origin доверены в CrossOrigin, с обоснованием.

## Альтернативы

- **Один список для всех клиентов, `Authorization` разрешён всем.**
  Меньше конфигурации, но веб-клиент сможет хранить bearer в JS, и
  исчезает знание «origin → аудитория».
- **Не доверять мобильным origin в CrossOrigin, а пропускать запросы с
  `Authorization` без cookie.** Эквивалентно по безопасности, но нужен
  свой код поверх stdlib-слоя и условие, которое легко ослабить при
  правке. Список доверенных origin — стандартный способ настройки
  `CrossOriginProtection`.
- **Свой hostname в оболочке** (`server.hostname` Capacitor, например
  `https://app.neurodent.local`). Origin становится «нашим» на вид, но
  подделать его может любое приложение, настроив тот же hostname. Защиты
  это не добавляет, только создаёт видимость.
- **Нативный HTTP в оболочке (`CapacitorHttp`) в обход CORS.** Возможен и
  не требует мобильной CORS-политики. Но решение об этом — в ТЗ
  фронтенда: мобильная политика ему не мешает, а без неё обычный `fetch`
  в оболочке не работает.

## Последствия

- Конфиг: `NEURODENT_CLINIC_WEB_ORIGINS`, `NEURODENT_PATIENT_WEB_ORIGINS`,
  `NEURODENT_MOBILE_ORIGINS` вместо `NEURODENT_CORS_ALLOWED_ORIGINS`.
  Новые проверки при старте с тестами на каждую.
- `middleware.CORS` получает две политики. Конфиг отдаёт Auth связку
  «origin → аудитория».
- `TestRouter_ErrorResponsesAreUniform` — для обеих политик. У мобильного
  origin на любом ответе нет `Allow-Credentials`.
- Новые кейсы:
  - preflight с мобильного origin и `Authorization` — 204;
  - preflight с веб-origin и `Authorization` — 403;
  - небезопасный запрос с мобильного origin проходит CrossOrigin;
  - небезопасный запрос с чужого origin — 403.
- Мутации:
  - `Allow-Credentials: true` для мобильных origin;
  - `Authorization` разрешён вебу;
  - нет проверки пересечения списков.
- Спроектировано по контролям:
  - HIPAA 45 CFR §164.312(c)(1);
  - ISO/IEC 27001:2022 A.8.26, A.8.28;
  - SOC 2 CC6.6.