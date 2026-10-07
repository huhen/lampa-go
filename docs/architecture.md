# Архитектура

## Обзор

lampa-go — один домен с path-based маршрутизацией: статика фронта, собственные ручки API и reverse-proxy на upstream cub живут на одном порту (`server.listen`, по умолчанию `:8080`). Сервер рассчитан на работу за TLS-reverse-proxy (см. [deploy.md](deploy.md)).

### Маршруты

Фактические маршруты из `internal/server/server.go` и `internal/api/api.go`:

| Маршрут | Методы | Что происходит |
|---|---|---|
| `/healthz` | GET | liveness: всегда `ok` |
| `/readyz` | GET | readiness: `db.PingContext` с таймаутом 2s; ошибка → 503 |
| `/cub/api/checker` | GET, POST | заглушка: GET → `ok`, POST → эхо первого form-значения (body до 1 MiB) |
| `/cub/api/plugins/blacklist` | GET | заглушка: `[]` |
| `/cub/api/metric/{rest...}` | GET | заглушка: `{"secuses":true}` |
| `/cub/api/ad/{rest...}` | GET | заглушка: `{"secuses":true}`; для `ad/vast` — расширенный payload с датой |
| `/cub/geo` и `/cub/geo/` | GET | код страны из заголовка `cub.geo_header` (по умолчанию `X-Geo-Country`), иначе `cub.geo_default` |
| `/cub/{rest...}` | любые | reverse-proxy на `cub.upstream` |
| `/` (всё остальное) | любые | статика из `server.static_dir`; чего нет — 404 |

Приоритет `http.ServeMux`: более специфичный паттерн выигрывает, поэтому заглушки перебивают прокси, а subtree-паттерн `/cub/geo/` (нужен, потому что overlay-плагин переписывает `geo.<домен>/` в `/cub/geo/` с завершающим слэшем) перебивает `/cub/{rest...}`. Под `/cub/geo/` ничего не живёт — отдаётся та же заглушка geo.

`/api/*` — резерв под собственное API приложения; в v1 отдельного обработчика нет, путь проваливается в статику и даёт 404.

## Middleware chain

Порядок (первый — внешний):

```
requestID → observe (trace + metrics + request log) → recover → mux
```

`requestID` — внешний, чтобы `X-Request-ID` был в каждом логе и был доступен всем обработчикам; `recover` — внутренний, чтобы паника в любом обработчике превратилась в 500, который `observe` (сидящий снаружи) всё ещё запишет в лог и метрики.

- **requestID** — читает клиентский `X-Request-ID` и принимает его только если он валиден: длина 1–128 байт, каждый байт — printable ASCII без пробела (`0x21..0x7e`). Иначе генерируется новый: 8 случайных байт → 16 hex-символов. Заголовок выставляется в ответ всегда, когда ID получен. ID кладётся в контекст (`requestIDKey`).
- **observe** — span на запрос, метрики и request-лог (см. [observability.md](observability.md)).
- **recover** — `recover()` → лог `panic in handler` + `500 internal server error`.

### statusWriter

`observe` оборачивает `http.ResponseWriter` в `statusWriter`, который запоминает статус. Для стриминга прокси критичны две детали:

- `Flush()` пробрасывается в обёрнутый writer — `http.Flusher`-assertions внутри `httputil.ReverseProxy` продолжают работать, стриминг не застревает;
- `Unwrap()` возвращает обёрнутый writer, так что `http.ResponseController` видит оригинал.

Таймауты самого `http.Server`: `ReadHeaderTimeout: 5s`, `IdleTimeout: 60s`, **`WriteTimeout` отсутствует намеренно** — проксируемые стримы нельзя обрывать по таймауту записи.

## Прокси (/cub/)

`internal/cubproxy` — `httputil.ReverseProxy` в режиме `Rewrite`:

- **Path-preserving**: `/cub/<suffix>` → `<upstream>/<suffix>`; префикс `/cub/` срезается, остальное переносится как есть.
- **Subdomain markers**: если первый сегмент suffix входит в `cub.subdomain_markers` (по умолчанию `tmdb`, `geo`, `ws`, `imagetmdb`, `cdn`, `ad`; сравнение case-insensitive), апстрим-хост становится `<marker>.<upstream-host>`, а маркер убирается из пути: `/cub/tmdb/3/movie/1` → `https://tmdb.<upstream-host>/3/movie/1`. Обратите внимание: маркер приклеивается к `upstream.Host` **вместе с портом** — для продакшена upstream должен быть без порта (или `:443`), чтобы `tmdb.<host>` резолвился корректно.
- **Схема** — всегда схема upstream (`pr.Out.URL.Scheme = p.upstream.Scheme`), строгая, без даунгрейда.
- **Anti-spoof**: `pr.SetXForwarded()`. Перед `Rewrite` stdlib сам удаляет клиентские `Forwarded`, `X-Forwarded-For`, `X-Forwarded-Host`, `X-Forwarded-Proto`; после этого `SetXForwarded` ставит `X-Forwarded-For` = адрес непосредственного пира, `X-Forwarded-Host` = клиентский `Host`, а `X-Forwarded-Proto` = `https`/`http` по признаку `In.TLS`. Так как lampa-go слушает plain HTTP, `X-Forwarded-Proto` к upstream уходит **всегда `http`** — см. замечание в [deploy.md](deploy.md).
- **Query** санитизируется stdlib'ом: перед `Rewrite` `RawQuery` прогоняется через `cleanQueryParams` (отбрасываются unparsable параметры — «голые» `;`, невалидные escape). Копировать `pr.In.URL.RawQuery` обратно нельзя — это отменило бы санитизацию.
- **Фильтрация response-заголовков** — обёртка над `http.RoundTripper` удаляет из ответа upstream: `server`, `content-security-policy`, `content-disposition` (точно) и всё с префиксами `x-`, `alt-`, `access-control-` (сравнение в lowercase). Клиенту не уходит ничего, что может раскрыть прокси или сломать CSP/скачивание.
- **Таймаут** (`cub.timeout`, по умолчанию `15s`) — `context.WithTimeout` вокруг всего обмена, включая прокачку body. Если таймаут истёк до заголовков — клиент получает `502 upstream unavailable`; если в середине стрима — соединение просто обрывается (502 после отправленных заголовков выдать невозможно). `FlushInterval: -1` — body отдаётся сразу, без буферизации.
- **ErrorHandler**: отмена клиентом (`context.Canceled`) — только debug-лог (клиент уже ушёл, отвечать некому); остальное — error-лог + `502 upstream unavailable`.

## Попадание фронтенда под /cub/

Фронт из коробки ходит на зеркала куба, перечисленные в `cub_mirrors` в `src/core/manifest.js` upstream (и их поддомены). Чтобы трафик попал на наш домен, в overlay лежит `public/plugins/modification.js`:

- Lampa автоматически загружает `plugins/modification.js` с хостинга (см. `src/core/plugins.js` upstream) — патчить загрузчик не нужно;
- плагин подписан на `Lampa.Listener.follow('request_before')` и переписывает URL к зеркалам куба **и к собственному домену** в `<origin>/cub/<marker?>/<path>` (маркер — тот же список: `tmdb`, `geo`, `ws`, `imagetmdb`, `cdn`, `ad`; учитываются и поддомены зеркал вида `tmdb.<зеркало>`);
- расчёт `origin` устойчив к старым webview (Orsay, ранний webOS), где нет `location.origin`.

Ожидаемый будущий патч `src/core/manifest.js` (`cub_mirrors = ['<наш домен>']`) сделает наш домен «родным» зеркалом: тогда URL будет строиться сразу на наш домен без runtime-переписывания. Патч **пока не создан** — боевой домен не выбран; как его создать, описано в [frontend-update.md](frontend-update.md).

> **Синхронизация списков.** Списки маркеров и зеркал продублированы в двух местах: `cub.subdomain_markers` в конфиге (их использует прокси) и `MARKERS`/`MIRRORS` в `frontend/overlay/public/plugins/modification.js` (их использует клиент). При смене зеркал upstream обновлять **оба** места; правка `modification.js` требует `make fe-update`, чтобы попасть в сборку.

## Хранилище

`internal/storage` открывает БД и накатывает goose-миграции (embedded в бинарник, `internal/storage/migrations/*.sql`).

**SQLite** (по умолчанию; драйвер `modernc.org/sqlite` — pure-Go, без cgo):

- pragmas передаются как параметры DSN (`_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)`), поэтому применяются **на каждом** соединении пула: `PRAGMA foreign_keys` — connection-scoped, а `database/sql` может молча подменить соединение после ошибки драйвера;
- `SetMaxOpenConns(1)` — single writer: у SQLite один писатель, одно соединение позволяет не ловить `SQLITE_BUSY` вместо ожидания на `busy_timeout`;
- отдельный `PRAGMA journal_mode=WAL` выполняется один раз при старте — как ранняя проверка доступности/путей, реальную конфигурацию несут DSN-параметры;
- каталог из DSN создаётся автоматически.

**PostgreSQL** (драйвер `pgx/v5/stdlib`, поверх `database/sql`): пул конфигурируется как есть — `SetMaxOpenConns`, `SetMaxIdleConns`, `SetConnMaxLifetime` со стандартной 0-семантикой (см. [configuration.md](configuration.md)).

Миграции: goose, диалект `sqlite3`/`postgres`, запуск при каждом старте (`goose.UpContext`). Первая миграция создаёт таблицу `app_meta` (key/value) и пишет `schema = 1`.

## Наблюдаемость

Кратко: span на каждый запрос (переименование по route-паттерну), счётчик `http.server.requests` и гистограмма `http.server.duration` с лейблами method+route+status, slog-логи с `request_id`, `route`, `trace_id`. По умолчанию всё выключено (noop-провайдеры). Подробности и подключение к VictoriaMetrics — [observability.md](observability.md).

## Принципы развития

- Новый внешний сервис (например, транскодинг) — либо новый префикс-модуль по образцу `cubproxy` (свой path-префикс, свой upstream), либо отдельный микросервис со своим поддоменом. Никакой логики «по хосту» внутри lampa-go не появляется — маршрутизация остаётся path-based.
- Заглушки cub API постепенно заменяются собственной реализацией по мере необходимости; контракт ответов фиксируется фактическим поведением upstream (см. забавное `secuses` — так отвечает сам upstream).
