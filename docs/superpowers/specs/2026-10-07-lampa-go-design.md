# Lampa-Go: дизайн первой итерации

- **Дата:** 2026-10-07
- **Статус:** approved (brainstorming завершён)
- **Референсы:** [lampac-nextgen/lampac](https://github.com/lampac-nextgen/lampac) (C#-образец конечного результата), [yumata/lampa-source](https://github.com/yumata/lampa-source) (исходники веб-интерфейса)

## 1. Цель и охват

Переработка C#-проекта lampac на Go. Полная функциональность не копируется — C#-версия является образцом конечного результата; Go-версия развивается итеративно с учётом видения владельца и особенностей языка.

**Первая итерация (этот spec):**

1. Сервинг веб-интерфейса Lampa (сборка из lampa-source).
2. Скелет API cub: каркас «свой ответ / прокси» + заглушки спец-ручек.
3. Скрипт обновления исходников фронта: сводка изменений → патчи → сборка → деплой.

**Не входит (будущие итерации):** собственная бизнес-логика аккаунта (bookmarks/timeline/storage), WebSocket-сокет, собственный tmdb-сервис как отдельный модуль, транскодинг, TorrServer, DLNA, WAF/лимиты, админка.

## 2. Принятые решения (зафиксированы владельцем)

| Решение | Выбор |
|---|---|
| HTTP-стек | чистый stdlib: `net/http` (роутинг Go 1.22+), `httputil.ReverseProxy` |
| Go | 1.27.1, современные практики языка, SOLID без фанатизма |
| Наблюдаемость | OpenTelemetry (traces/metrics/logs) → OTLP gRPC → VictoriaMetrics |
| БД | SQLite3 (по умолчанию) или PostgreSQL 18; миграции goose |
| Postgres | connection pool: pgx/v5 stdlib-режим поверх `database/sql` с настройками пула |
| Схема API | сервис-первым: `/cub/{...}`, `/tmdb/{...}`, ... — не `/api/{сервис}/*` |
| Wildcard DNS | не используется; маршрутизация strictly path-based на одном домене; при конфликте путей или потребности разделения (например транскодинг) сервис выделяется в отдельный микросервис со своим поддоменом |
| Обновление фронта | вручную скриптом (локально/CI); сервер только раздаёт готовую папку |
| Документация | русский язык, технические термины — на английском; код и комментарии — только английский |
| Развёртывание | за reverse-proxy (nginx/Caddy), сервер слушает HTTP на локальном порту |

## 3. Обоснование схемы `/cub/{...}`

Трафик куба не ограничен `/api/*` — фронтенд ходит на корень cub-домена за разными путями:

- `/api/users/get`, `/api/bookmarks/*` — JSON API;
- `/img/profiles/*.png`, `/img/background/default.mp4` — профили, скринсейвер;
- `/extensions/<id>` — CSS тем и скринсейверов;
- `/plugin/iptv|shots|sport` — JS-скрипты плагинов;
- `tmdb.<домен>/3/movie/...`, `tmdb.<домен>/top/fire`, `/search/...` — TMDB-совместимое зеркало.

Префикс-сервис (`/cub/` = сервис, суффикс = оригинальный путь) покрывает всё единым правилом, устраняет конфликты со статикой фронта и масштабируется на будущие сервисы (`/tmdb/`, `/jacred/`, ...). Схема соответствует CubProxy из C#-версии (`/cub/{*suffix}`).

## 4. Структура репозитория

```
lampa-go/
├── cmd/lampa-go/            # main: сборка зависимостей, запуск, graceful shutdown
├── internal/
│   ├── config/              # YAML-конфиг, дефолты, валидация
│   ├── server/              # сборка HTTP-сервера, middleware chain
│   ├── web/                 # раздача статики фронта (fs, index.html, cache headers)
│   ├── api/                 # собственные ручки: healthz/readyz + заглушки cub
│   ├── cubproxy/            # прокси: ReverseProxy, header filtering, subdomain-маркеры
│   ├── obs/                 # OpenTelemetry setup (traces/metrics/logs, noop по умолчанию)
│   └── storage/             # database/sql (sqlite|postgres), goose-миграции (embedded)
├── frontend/
│   ├── patches/             # наши патчи: unified diff, применяются git apply
│   ├── overlay/             # файлы поверх исходников без diff-механики (plugins/modification.js)
│   ├── sources/             # рабочая копия lampa-source — в .gitignore
│   └── ORIGIN_COMMIT        # upstream-коммит последнего применённого обновления
├── scripts/
│   └── update-frontend.sh   # пайплайн обновления фронта
├── migrations/              # goose .sql, подключаются через //go:embed
├── deploy/web/              # каталог деплоя фронта — в .gitignore
├── docs/                    # документация (русский)
├── config.example.yaml
└── Makefile
```

Зависимости между пакетами — только вниз (`server` → `api`/`cubproxy`/`web`/`obs`/`storage`); взаимодействие через маленькие интерфейсы, декларируемые потребителем.

## 5. HTTP-сервер

### 5.1 Middleware chain

RequestID → OTel trace context → slog-логирование (с `trace_id`) → Recover → маршрутизация. Реализация на stdlib, без внешних middleware-библиотек.

### 5.2 Маршрутизация

| Паттерн | Кто отвечает | Поведение |
|---|---|---|
| `GET /healthz` | свои | liveness |
| `GET /readyz` | свои | ping БД |
| `GET/POST /cub/api/checker` | заглушка | `ok` (GET) / echo первого form-параметра (POST, `application/x-www-form-urlencoded`) |
| `GET /cub/api/plugins/blacklist` | заглушка | `[]` |
| `/cub/api/metric/*` | заглушка | `{"secuses":true}` |
| `/cub/api/ad/*` | заглушка | пустая реклама: `{"secuses":true,"ad":[],...}` |
| `/cub/geo` | заглушка | страна клиента: из `{cub.geo_header}` (проставляет reverse-proxy), при отсутствии — `{cub.geo_default}` |
| `/cub/tmdb./...`, `/cub/geo./...` | прокси | маркер поддомена в первом сегменте → `tmdb.<upstream-host>/...` (приём CubProxy `GetDomain`; работает на одном домене, без wildcard DNS) |
| `/cub/{остальное}` | прокси | path-preserving → `{cub.upstream}/{suffix}` |
| `/api/*` | резерв | под собственный серверный API (в v1 не занят) |
| `/*` | web | статика фронта из `{server.static_dir}` |

Свои заглушки регистрируются раньше catch-all прокси и всегда имеют приоритет.

### 5.3 Прокси-подсистема (`internal/cubproxy`)

`httputil.ReverseProxy` с `Rewrite`-хуком:

- подмена scheme/host на upstream; суффикс пути передаётся как есть;
- вырезание hop-by-hop заголовков, `X-Forwarded-For`, `Host` = upstream;
- фильтрация response-заголовков (как в C#): `server`, `transfer-encoding`, `content-security-policy`, `content-disposition`, префиксы `x-*`, `alt-*`, `access-control*`;
- таймаут `{cub.timeout}` через `context`; стриминг тела без буферизации (`FlushInterval = -1`);
- без ретраев, circuit breaker и кеша — YAGNI для каркаса.

Поддоменные маркеры: первый сегмент пути из множества `{tmdb., geo., ws., imagetmdb., cdn., ad.}` → поддомен приписывается к хосту upstream (`/cub/tmdb./3/x` → `https://tmdb.cub.best/3/x`). Множество — конфиг `{cub.subdomain_markers}`.

### 5.4 Статика (`internal/web`)

`http.FileServer` поверх `os.DirFS({server.static_dir})`; `/` → `index.html`; `Cache-Control: no-cache` для `index.html` и `assembly.json`, `Cache-Control: public, max-age=86400` для остальных файлов; `ETag/Last-Modified` штатно. SPA-fallback нет — unknown paths отдают 404.

### 5.5 Попадание фронтенда под `/cub/`

Два механизма (создаются скриптом обновления, см. §7):

1. **Патч `src/core/manifest.js`** — `cub_mirrors = ['{server.base_domain}']`: mirrors-checker фронтенда пингует наш `/cub/api/checker`, домен не «уплывает».
2. **`plugins/modification.js`** (overlay, фронтенд грузит его сам с хостинга) — перехват `Lampa.Listener.follow('request_before')` и переписывание URL зеркал куба → `{protocol}//{host}/cub/<path+query>` (scheme и host срезаются; `tmdb.`-поддомен сохраняется первым сегментом маркера).

WebSocket Lampa (`wss://<soc_mirrors>:8443`) в v1 не трогается — фронтенд ходит на soc-зеркала куба напрямую.

## 6. Скрипт обновления фронта

`scripts/update-frontend.sh`, цели Makefile: `fe-update` (полный пайплайн), `fe-diff` (только сводка, dry-run), `fe-build` (сборка текущего sources), `fe-deploy` (деплой собранного), `fe-new-patch NAME=NNN-slug` (создание нового патча).

Пайплайн `fe-update`:

1. **Fetch** — `frontend/sources/` хранит pristine git-clone `yumata/lampa-source`; `git fetch` нового `upstream/main`.
2. **Сводка изменений** — `git diff --stat {ORIGIN_COMMIT}..FETCH_HEAD` + список изменённых файлов + перекрёстная проверка: если upstream изменил файл, затрагиваемый каким-либо патчем, — предупреждение «патч NNN вероятно конфликтует».
3. **Патчи** — checkout нового коммита; каждый `frontend/patches/NNN-*.patch` в лексическом порядке: `git apply --check`, затем `git apply`. Первый неприменившийся — стоп с отчётом (патч, hunks), `ORIGIN_COMMIT` не обновляется.
4. **Overlay** — рекурсивное копирование `frontend/overlay/` поверх sources.
5. **Сборка** — `npm ci` (если изменился lock-файл) → `npx gulp pack_github` → `build/github/lampa/`.
6. **Деплой** — атомарно: результат в tmp-каталог рядом с `deploy/web/` → rename. Запись нового `ORIGIN_COMMIT`. Итоговая сводка: версия, статистика, применённые патчи.

В git живут только `patches/`, `overlay/`, `ORIGIN_COMMIT`; `sources/` и `deploy/web/` — в `.gitignore`.

Патчи — единый механизм точечных доработок (списки доменов и т.п.); overlay — для добавляемых целиком файлов (`modification.js`, статические ассеты).

## 7. Конфигурация

`config.yaml` (gopkg.in/yaml.v3), путь — флаг `-config`, дефолты в коде, валидация на старте.

```yaml
server:
  listen: ":8080"
  static_dir: ./deploy/web
  base_domain: lampa.example.com   # наш домен (подставляется в патчи/rewrite)
cub:
  upstream: https://cub.best
  timeout: 15s
  geo_header: X-Geo-Country
  geo_default: US
  subdomain_markers: ["tmdb.", "geo.", "ws.", "imagetmdb.", "cdn.", "ad."]
db:
  driver: sqlite                    # sqlite | postgres
  dsn: ./data/lampa-go.db
  max_open_conns: 25                # sqlite форсирует 1 (single writer)
  max_idle_conns: 5
  conn_max_lifetime: 30m
otel:
  enable: false
  endpoint: localhost:4317          # OTLP gRPC → vmagent/collector → VictoriaMetrics
  service_name: lampa-go
  insecure: true
log:
  level: info
  format: text                      # text | json
```

## 8. Наблюдаемость (`internal/obs`)

`Setup(ctx, cfg) → (shutdown func, error)`; три сигнала через единый OTLP gRPC endpoint:

- **Traces** — middleware создаёт span на запрос (route pattern, method, status code);
- **Metrics** — `http_requests_total`, `http_request_duration_seconds` (route, method, status) через otel meter;
- **Logs** — stdlib `log/slog`; middleware прокидывает `trace_id` в контекст логгера; OTel logs bridge для отправки наружу.

`otel.enable: false` → noop tracer/meter/provider, slog в stdout (text|json) — нулевые накладные расходы.

## 9. Хранилище (`internal/storage`)

- `Open(cfg) (*sql.DB, error)`: `sqlite` → `modernc.org/sqlite` (pure Go, кросс-компиляция без cgo; WAL, `MaxOpenConns(1)` — single writer), `postgres` → `jackc/pgx/v5` stdlib-режим; пул `database/sql` настраивается из конфига (`max_open_conns`, `max_idle_conns`, `conn_max_lifetime`).
- Миграции: `pressly/goose/v3`, `//go:embed migrations/*.sql`, `goose.Up` при старте.
- Первая миграция `00001_init.sql`: `app_meta(key TEXT PRIMARY KEY, value TEXT)` — проверка пайплайна на обоих драйверах. Схемы аккаунтов/закладок — будущие итерации.
- `/readyz` пингует БД.

## 10. Тестирование

- Хендлеры и роутер — `httptest`; прокси — `httptest.Server` как fake upstream: path-preserving, фильтрация заголовков, subdomain-маркеры, приоритет заглушек над прокси, таймаут.
- Конфиг — golden-тесты дефолтов и валидации.
- `make smoke` — запуск бинарника на тестовом порту с tmp-статикой + curl по ключевым ручкам (healthz, readyz, checker, blacklist, metric, статика, прокси на fake upstream).
- Скрипт обновления — `fe-diff` (dry-run) + ручная проверка первого реального обновления.

## 11. Документация

Код, идентификаторы, комментарии — только английский; документация — русский язык, термины (reverse-proxy, span, migration, ...) на английском.

- `README.md` — обзор, быстрый старт;
- `docs/architecture.md` — компоненты, маршрутизация, схема запросов;
- `docs/frontend-update.md` — цикл обновления, создание патчей, разрешение конфликтов;
- `docs/configuration.md` — все ключи конфига;
- `docs/observability.md` — подключение к VictoriaMetrics;
- `docs/deploy.md` — пример reverse-proxy (Caddy/nginx).

## 12. Зависимости (go.mod)

`go.opentelemetry.io/otel` (+ `sdk`, OTLP exporters trace/metric/log), `pressly/goose/v3`, `modernc.org/sqlite`, `jackc/pgx/v5`, `gopkg.in/yaml.v3`. Остальное — stdlib.

## 13. Принципы на будущее

- Новый внешний сервис = новый модуль-префикс (`/tmdb/`, ...) с тем же интерфейсом (`name`, `upstream`, свои перехваты) либо отдельный микросервис со своим поддоменом — при конфликте путей или потребности разделения нагрузки (транскодинг).
- Заглушки постепенно заменяются собственной реализацией (аккаунт, bookmarks, timeline, storage) на базе `internal/storage`.
- Платформенные сборки (webOS, Tizen, MSX) добавляются без изменения фундамента: общие патчи `src/` действуют на все платформы, в пайплайн обновления добавляются gulp-таски (`pack_webos`, `pack_tizen`), плюс точечный патч `index/<платформа>/loader.js` на наш домен; при необходимости — модуль упаковки `lg.ipk`/`samsung.wgt` на стороне сервера.
- WebSocket-сокет, WAF-лимиты, кеш прокси — кандидаты в последующие итерации.
