# Переход на lampa-web-builder + контейнеризация — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** lampa-go получает веб-интерфейс только через API lampa-web-builder; локальный пайплайн сборки фронта удалён; lampa-go контейнеризован и поднят в docker-compose вместе с билдером.

**Architecture:** интеграция с билдером уже реализована (`internal/builder`: клиент API, versioned-deployer с symlink-swap, reconcile-воркер) — меняем только дефолты конфига (`builder.enabled: true`, `static_dir` = symlink-путь), удаляем старый пайплайн (`frontend/`, `scripts/update-frontend.sh`, `fe-*`), обновляем документацию и добавляем Dockerfile (distroless), полный compose-стек и CI-workflow с публикацией в GHCR. Спек: `docs/superpowers/specs/2026-10-30-builder-only-frontend-design.md`.

**Tech Stack:** Go 1.27 (stdlib + modernc/sqlite — pure Go, `CGO_ENABLED=0` даёт статичный бинарник), distroless/static, Docker + docker compose, GitHub Actions (pattern репозитория lampa-web-builder).

**Ветка:** `main` (разрешение владельца: первичная разработка до первого минимального результата — в main). В репозитории пока только один коммит (спек) — весь код не закоммичен, Task 1 фиксирует его как initial import **без** старого пайплайна (в историю он не попадает).

**Проверка окружения:** Go 1.27 (`go version`), docker (`docker info`), для smoke — `python3`.

---

### Task 1: Удаление старого пайплайна + initial import кодовой базы

**Files:**
- Delete: `frontend/` (весь каталог, включая локальный `sources/`), `scripts/update-frontend.sh`, `docs/frontend-update.md`, `docs/superpowers/plans/` (оба старых плана)
- Modify: `Makefile` (цели `fe-*`, `export FE_DEPLOY_DIR`), `.gitignore` (строки frontend)

- [ ] **Step 1: Удалить файлы старого пайплайна**

```bash
cd /home/usr1/coding/lampa-go
rm -rf frontend
rm docs/superpowers/plans/2026-10-07-lampa-go-first-iteration.md \
   docs/superpowers/plans/2026-10-07-builder-integration.md
rm scripts/update-frontend.sh docs/frontend-update.md
```

(Текущий план `2026-10-30-builder-only-frontend.md` в `docs/superpowers/plans/` **не удалять** — только два старых файла.)

- [ ] **Step 2: Переписать Makefile без fe-целей**

Полное новое содержимое `Makefile`:

```make
GO ?= go
BINARY := bin/lampa-go

.PHONY: all build run test vet fmt smoke clean

all: build

build:
	$(GO) build -o $(BINARY) ./cmd/lampa-go

run: build
	./$(BINARY) -config config.yaml

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

smoke: build
	./scripts/smoke.sh

clean:
	rm -rf bin
```

(ВНИМАНИЕ: строки рецептов — с **табу** в начале.)

- [ ] **Step 3: Почистить .gitignore**

Удалить две строки: `/frontend/sources/` и `frontend/package-lock.json.bak`. Остальное (`/bin/`, `/data/`, `/deploy/`, `*.db*`, `.idea/`, `/config.yaml`, `/.env`) остаётся.

- [ ] **Step 4: Убедиться, что тесты зелёные (ничего в Go-коде на удалённое не ссылается)**

Run: `go vet ./... && go test ./...`
Expected: vet молчит, все пакеты `ok`.

- [ ] **Step 5: Initial import в git**

```bash
git add -A
git commit -m "chore: initial import кодовой базы без локального пайплайна фронта"
```

Expected: коммит создан; `git status` пуст (кроме gitignored).

---

### Task 2: Конфиг — builder включён по умолчанию (TDD)

**Files:**
- Modify: `internal/config/config_test.go` (`TestDefaultsAreValid`, `TestBuilderDefaults`)
- Modify: `internal/config/config.go` (`Defaults()`)
- Modify: `config.example.yaml`

- [ ] **Step 1: Обновить падающие тесты**

В `internal/config/config_test.go` в начале `TestDefaultsAreValid` (после `cfg := Defaults()`) добавить две строки — включённый по умолчанию builder требует `api_key` и `base_domain`:

```go
	// Builder is enabled by default: api_key and base_domain become required.
	cfg.Builder.APIKey = "test-key"
	cfg.Server.BaseDomain = "lampa.example.com"
```

Полностью заменить `TestBuilderDefaults` (он ожидает `enabled: false`):

```go
func TestBuilderDefaults(t *testing.T) {
	cfg := Defaults()
	if !cfg.Builder.Enabled {
		t.Error("builder must be enabled by default")
	}
	if cfg.Server.StaticDir != "./deploy/web/current" {
		t.Errorf("static_dir = %q, want ./deploy/web/current", cfg.Server.StaticDir)
	}
	if cfg.Builder.URL != "http://builder:8080" {
		t.Errorf("builder url = %q, want http://builder:8080", cfg.Builder.URL)
	}
	if cfg.Builder.PollInterval.Std() != 5*time.Minute {
		t.Errorf("poll_interval = %v, want 5m", cfg.Builder.PollInterval.Std())
	}
	if cfg.Builder.KeepVersions != 3 {
		t.Errorf("keep_versions = %d, want 3", cfg.Builder.KeepVersions)
	}
	// Enabled by default: api_key and base_domain become required.
	if err := cfg.Validate(); err == nil {
		t.Error("defaults must not validate without builder.api_key")
	}
	cfg.Builder.APIKey = "k"
	cfg.Server.BaseDomain = "lampa.example.com"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("must validate with api_key and base_domain: %v", err)
	}
}
```

- [ ] **Step 2: Убедиться, что тесты падают**

Run: `go test ./internal/config/ -run 'TestBuilderDefaults|TestDefaultsAreValid' -v`
Expected: FAIL (`builder must be enabled by default`, `static_dir = "./deploy/web"`).

- [ ] **Step 3: Изменить Defaults()**

В `internal/config/config.go` в `Defaults()`:

```go
		Server: Server{Listen: ":8080", StaticDir: "./deploy/web/current"},
```

и в блоке `Builder:` первой строкой добавить `Enabled`:

```go
		Builder: Builder{
			Enabled:      true,
			URL:          "http://builder:8080",
			PollInterval: Duration(5 * time.Minute),
			KeepVersions: 3,
		},
```

- [ ] **Step 4: Убедиться, что тесты проходят**

Run: `go test ./... && go vet ./...`
Expected: все пакеты `ok`, vet молчит.

- [ ] **Step 5: Обновить config.example.yaml**

Полное новое содержимое `config.example.yaml` (комментарии — английский, правило репозитория):

```yaml
# lampa-go configuration example.
# Copy to config.yaml and adjust. All keys have built-in defaults except
# `server.base_domain` and `builder.api_key`: both are required while
# `builder.enabled` is true, which is the default (the builder is the only
# frontend source; disable it only for local runs without one). Unknown keys
# are rejected at startup (strict parsing).

server:
  listen: ":8080"            # address to listen on (behind a reverse proxy)
  static_dir: ./deploy/web/current  # symlink path managed by the builder worker;
                                    # versions live in <static_dir>/../versions/
  base_domain: lampa.example.com    # our public domain; the build domain

cub:
  upstream: https://cub.example # upstream the /cub/* requests are forwarded to (built-in default: see internal/config)
  timeout: 15s               # caps the whole exchange incl. the response body
  geo_header: X-Geo-Country  # client country header set by the reverse proxy
  geo_default: US            # fallback when the header is missing
  subdomain_markers: ["tmdb", "geo", "ws", "imagetmdb", "cdn", "ad"]

builder:
  enabled: true              # the only frontend source; set false only for
                             # local runs without a builder (smoke, debugging)
  url: http://builder:8080   # builder API (compose service name; loopback: http://127.0.0.1:8081)
  api_key: change-me         # X-API-Key for the builder (required when enabled)
  poll_interval: 5m          # how often to compare available_commit with ours
  keep_versions: 3           # extracted versions to keep under <static_dir>/../versions

db:
  driver: sqlite             # sqlite | postgres
  dsn: ./data/lampa-go.db    # or postgres://user:pass@host/lampa
  max_open_conns: 25         # postgres pool; sqlite forces 1 (single writer)
  max_idle_conns: 5          # 0 means "no idle connections" (database/sql semantics)
  conn_max_lifetime: 30m     # 0 means "reuse forever"

otel:
  enable: false              # false -> noop providers, logs to stdout
  endpoint: localhost:4317   # OTLP gRPC host:port (vmagent / collector -> VictoriaMetrics)
  service_name: lampa-go
  insecure: true             # no TLS on the OTLP connection

log:
  level: info                # debug | info | warn | error
  format: text               # text | json
```

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go config.example.yaml
git commit -m "feat: builder.enabled по умолчанию; static_dir — symlink-путь deploy/web/current"
```

---

### Task 3: smoke.sh — явное отключение builder

**Files:**
- Modify: `scripts/smoke.sh` (heredoc `$TMP/config.yaml`)

- [ ] **Step 1: Отключить builder в smoke-конфиге**

В `scripts/smoke.sh` в heredoc `cat > "$TMP/config.yaml" <<EOF` после блока `server:` (перед `cub:`) вставить:

```yaml
builder:
  enabled: false
```

(Включённый по умолчанию builder потребовал бы `api_key` и живой билдер — smoke работает без них.)

- [ ] **Step 2: Прогнать smoke**

Run: `make smoke`
Expected: все проверки `ok ...`, в конце — успешное завершение скрипта (graceful shutdown тоже проверяется).

- [ ] **Step 3: Commit**

```bash
git add scripts/smoke.sh
git commit -m "test: smoke — явное отключение builder"
```

---

### Task 4: README — builder как единственный источник фронтенда

**Files:**
- Modify: `README.md` (полная замена)

- [ ] **Step 1: Переписать README**

Полное новое содержимое `README.md`:

````markdown
# lampa-go

## Что это

**lampa-go** — Go-сервер, который хостит веб-интерфейс [Lampa](https://github.com/yumata/lampa-source) (собирается сервисом [lampa-web-builder](https://github.com/huhen/lampa-web-builder)) и реализует скелет API **cub** по схеме «свой ответ / прокси». Это переработка (порт) C#-проекта [lampac](https://github.com/immisteriou/lampac), который остаётся образцом конечного результата.

Идея — **один домен**: сайт обращается только к нам, больше ничего настраивать на клиентах не нужно. Часть ручек cub API сервер отвечает сам (заглушки: checker, blacklist, metric, ads, geo), остальное прозрачно проксируется на настраиваемый upstream (`cub.upstream`; встроенный дефолт — см. `Defaults()` в `internal/config/config.go`).

## Возможности

- **Фронтенд из [lampa-web-builder](https://github.com/huhen/lampa-web-builder)** — единственный источник веб-интерфейса. Воркер сервера сверяет свой задеплоенный коммит с `available_commit` билдера, заказывает сборку `server.base_domain`, скачивает архив и атомарно выкладывает его (symlink-swap, без окна 404). Требуются `builder.url` и `builder.api_key`.
- **Статика** из `server.static_dir` (по умолчанию `./deploy/web/current` — symlink на текущую версию): `index.html` и `assembly.json` с `Cache-Control: no-cache`, остальное — на сутки; листинг каталогов отключён.
- **Заглушки cub API**: `/cub/api/checker`, `/cub/api/plugins/blacklist`, `/cub/api/metric/...`, `/cub/api/ad/...`, `/cub/geo` — ответы формируются локально.
- **Прокси `/cub/*`**: path-preserving reverse-proxy на `httputil.ReverseProxy` с subdomain markers (`/cub/tmdb/...` → `tmdb.<upstream-host>/...`), фильтрацией response-заголовков и общим таймаутом на обмен.
- **OpenTelemetry**: traces + metrics + logs через OTLP gRPC; по умолчанию выключен (noop-провайдеры).
- **SQLite** (по умолчанию, pure-Go драйвер modernc) **или PostgreSQL** (pgx stdlib) + goose-миграции, embedded в бинарник.

## Быстрый старт

### Стек в docker-compose (рекомендуемый)

```bash
cp .env.example .env                 # BUILDER_API_KEY, BUILDER_DEFAULT_DOMAIN
cp config.example.yaml config.yaml   # builder.api_key = BUILDER_API_KEY,
                                     # server.base_domain = BUILDER_DEFAULT_DOMAIN
docker compose up -d                 # builder + lampa-go
```

Контейнерный `config.yaml` (пути внутри контейнера) — в [docs/deploy.md](docs/deploy.md#compose). Фронтенд появится автоматически после первой сборки билдера (тестовая сборка на `BUILDER_DEFAULT_DOMAIN`, затем сборка по заказу lampa-go).

### Бинарник на хосте

Требования: **Go 1.27+**. Билдер поднимается отдельно: `docker compose up -d builder`; в `config.yaml` при этом `builder.url: http://127.0.0.1:8081` и в `docker-compose.yml` раскомментирована loopback-публикация порта билдера.

```bash
cp config.example.yaml config.yaml   # builder.api_key, server.base_domain
make run                             # = make build && ./bin/lampa-go -config config.yaml
```

Открыть `http://localhost:8080`.

Проверка работоспособности без билдера и внешней сети: `make smoke` (нужен `python3`; в smoke-конфиге `builder.enabled: false`).

## Структура репозитория

```
cmd/lampa-go/          точка входа: флаги, сигналы, graceful shutdown
internal/server/       сборка HTTP-сервера: маршрутизация и middleware chain
internal/api/          свои ручки: /healthz, /readyz и заглушки cub API
internal/cubproxy/     reverse-proxy /cub/* на upstream
internal/web/          раздача статики (cache-control, без листинга)
internal/builder/      интеграция с lampa-web-builder: клиент API, деплойер, воркер
internal/storage/      sqlite/postgres + goose-миграции (internal/storage/migrations)
internal/obs/          OpenTelemetry (traces/metrics/logs) + slog
internal/config/       YAML-конфиг: defaults, строгий разбор, валидация
scripts/               smoke.sh
deploy/                версии фронта от воркера: versions/<commit>/ + current (в git не входит)
docs/                  документация
```

## Команды

| Цель | Что делает |
|---|---|
| `make build` | собрать `bin/lampa-go` |
| `make run` | собрать и запустить с `-config config.yaml` |
| `make test` | `go test ./...` |
| `make vet` | `go vet ./...` |
| `make fmt` | `go fmt ./...` |
| `make smoke` | собрать и прогнать `scripts/smoke.sh` (эндпоинты + прокси + graceful shutdown) |
| `make clean` | удалить `bin/` |

## Документация

- [docs/architecture.md](docs/architecture.md) — маршруты, middleware, прокси, хранилище
- [docs/configuration.md](docs/configuration.md) — все ключи конфигурации
- [docs/observability.md](docs/observability.md) — сигналы и подключение к VictoriaMetrics
- [docs/deploy.md](docs/deploy.md) — reverse-proxy, systemd, compose-стек, обновление в бою
````

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: README — builder как единственный источник фронтенда"
```

---

### Task 5: configuration.md и architecture.md

**Files:**
- Modify: `docs/configuration.md` (таблицы `server` и `builder`)
- Modify: `docs/architecture.md` (раздел «Попадание фронтенда под /cub/»)

- [ ] **Step 1: configuration.md — таблица server**

Заменить строки `static_dir` и `base_domain` на:

```markdown
| `server.static_dir` | string | `./deploy/web/current` | путь **symlink'а** на текущую версию фронта; версии раскладывает builder-воркер (`<корень static_dir>/versions/<commit>/`); при `builder.enabled: false` — обычный каталог со статикой, создаётся при старте |
| `server.base_domain` | string | *(пусто)* | наш публичный домен; обязателен при `builder.enabled: true` — домен заказываемой сборки |
```

- [ ] **Step 2: configuration.md — секция builder**

В таблице `builder` заменить строку `enabled` на:

```markdown
| `enabled` | `true` | включить воркер обновления фронта из билдера; отключать только для локальных прогонов без билдера (smoke, отладка) |
```

И в абзаце после таблицы фразу «При `enabled: true` обязательны `server.base_domain` — им становится домен заказываемой сборки.» заменить на:

```markdown
Включён по умолчанию; при `enabled: true` обязательны `builder.api_key` и
`server.base_domain` — им становится домен заказываемой сборки.
```

- [ ] **Step 3: architecture.md — раздел «Попадание фронтенда под /cub/»**

Заменить вводную фразу «Чтобы трафик попал на наш домен, в overlay лежит `public/plugins/modification.js`:» на «Чтобы трафик попал на наш домен, в репозитории [lampa-web-builder](https://github.com/huhen/lampa-web-builder) два механизма. Первый — плагин `overlay/public/plugins/modification.js` (фронтенд грузит его сам с хостинга):»

Заменить абзац «Ожидаемый будущий патч `src/core/manifest.js` … описано в [frontend-update.md](frontend-update.md).» на:

```markdown
Второй — патч `020-cub-domains.patch` в репозитории билдера: он прописывает `cub_mirrors = ['{{CUB_DOMAIN}}']` в `src/core/manifest.js`, а плейсхолдер подставляется **на этапе сборки** значением `server.base_domain` из заказа сборки. Наш домен становится «родным» зеркалом: URL строится сразу на наш домен, `modification.js` остаётся runtime-страховкой для ссылок на сторонние зеркала.
```

Заменить заметку «> **Синхронизация списков.** …» на:

```markdown
> **Синхронизация списков.** Списки маркеров и зеркал продублированы в двух местах: `cub.subdomain_markers` в конфиге (их использует прокси) и `MARKERS`/`MIRRORS` в `overlay/public/plugins/modification.js` репозитория [lampa-web-builder](https://github.com/huhen/lampa-web-builder) (их использует клиент). При смене зеркал upstream обновлять **оба** места; правка `modification.js` делается в репозитории билдера — следующая сборка подхватит её автоматически.
```

- [ ] **Step 4: Проверить отсутствие битых ссылок**

Run: `grep -rn 'frontend-update\|fe-update\|fe-build\|fe-diff\|fe-deploy\|fe-new-patch\|make fe' README.md docs/ ; echo "exit=$?"`
Expected: `exit=1` (ничего не найдено) — в README/docs не осталось ссылок на удалённый пайплайн. (На old spec этот grep не распространяется — его правим в Task 7.)

- [ ] **Step 5: Commit**

```bash
git add docs/configuration.md docs/architecture.md
git commit -m "docs: configuration/architecture — builder вместо fe-пайплайна"
```

---

### Task 6: deploy.md — compose-стек и обновление

**Files:**
- Modify: `docs/deploy.md` (раздел «Обновление», подраздел «compose», удалить «Миграция с плоской раскладки»)

- [ ] **Step 1: Заменить раздел «## Обновление»**

Всё содержимое от заголовка `## Обновление` до заголовка `## Обновление фронта через билдер` (включая `make fe-update` и абзац про rename-подмену) заменить на:

````markdown
## Обновление

Фронтенд обновляется автоматически воркером из lampa-web-builder (см. ниже) —
ручных шагов обновления фронта нет.

Go-код (бинарник на хосте):

```bash
make build
systemctl restart lampa-go
```

`systemctl restart` шлёт `SIGTERM` — срабатывает штатный graceful shutdown (см. unit выше).

Go-код (compose):

```bash
docker compose pull
docker compose up -d
```
````

- [ ] **Step 2: Заменить подраздел «### compose»**

Всё содержимое подраздела `### compose` (текст про «пока lampa-go работает бинарником») заменить на:

````markdown
`docker-compose.yml` в корне репозитория поднимает полный стек: `builder`
(порт наружу не публикуется, доступен по имени сервиса `builder`) и `lampa-go`
(`127.0.0.1:8080` за reverse-proxy). Ключи — в `.env` (шаблон `.env.example`):
`BUILDER_API_KEY` должен совпадать с `builder.api_key` в `config.yaml`,
`BUILDER_DEFAULT_DOMAIN` — с `server.base_domain`. Конфиг монтируется
read-only, данные (БД + версии фронта) живут в volume `lampa-data`.

Контейнерный `config.yaml` (пути — внутри контейнера):

```yaml
server:
  listen: ":8080"
  static_dir: /var/lib/lampa-go/web/current
  base_domain: lampa.example.com
builder:
  url: http://builder:8080
  api_key: <тот же BUILDER_API_KEY>
db:
  dsn: /var/lib/lampa-go/lampa-go.db
```
````

- [ ] **Step 3: Удалить подраздел «### Миграция с плоской раскладки»**

Удалить весь подраздел (от `### Миграция с плоской раскладки` до конца файла) — плоской раскладки больше нет ни в коде, ни в истории.

- [ ] **Step 4: Commit**

```bash
git add docs/deploy.md
git commit -m "docs: deploy — compose-стек и обновление; без плоской раскладки"
```

---

### Task 7: Спек первой итерации — вычистить старый пайплайн

**Files:**
- Modify: `docs/superpowers/specs/2026-10-07-lampa-go-design.md`

- [ ] **Step 1: Разметить замены (точечно, остальной текст не трогать)**

1. В §1 пункт 3 «Скрипт обновления исходников фронта: сводка изменений → патчи → сборка → деплой.» заменить на:
   «Получение собранного фронта: автоматически из [lampa-web-builder](https://github.com/huhen/lampa-web-builder) через API (см. спек `2026-10-30-builder-only-frontend-design.md`).»
2. В §2 таблицу решений: строку «| Обновление фронта | вручную скриптом (локально/CI); сервер только раздаёт готовую папку |» заменить на «| Обновление фронта | автоматически воркером из lampa-web-builder (API); сервер раздаёт распакованные версии |».
3. В §4 (структура) удалить блоки `frontend/` (4 строки) и строку `│   └── update-frontend.sh   # пайплайн обновления фронта` вместе с каталогом `scripts/` (если после удаления нечего не остаётся — удалить и `scripts/`); в блок `internal/` добавить `│   ├── builder/             # интеграция с lampa-web-builder: клиент, деплойер, воркер`; строку про `deploy/web/` заменить на `├── deploy/web/              # версии фронта: versions/<commit>/ + current (symlink) — в .gitignore`.
4. В §5.5 фразу «Два механизма (создаются скриптом обновления, см. §7):» заменить на «Два механизма (реализованы в репозитории [lampa-web-builder](https://github.com/huhen/lampa-web-builder), см. §6):»; пункт 1 заменить на «1. **Патч `src/core/manifest.js`** (`020-cub-domains.patch` в билдере) — `cub_mirrors = ['{{CUB_DOMAIN}}']`, плейсхолдер подставляется доменом `{server.base_domain}` при заказе сборки: mirrors-checker фронтенда пингует наш `/cub/api/checker`, домен не «уплывает».»; пункт 2: «(overlay, фронтенд грузит его сам с хостинга)» оставить, уточнить источник — «(overlay билдера, фронтенд грузит его сам с хостинга)».
5. Весь раздел «## 6. Скрипт обновления фронта» (до `## 7.`) заменить на:

```markdown
## 6. Получение фронта (lampa-web-builder)

Веб-интерфейс собирается вне lampa-go: сервис [lampa-web-builder](https://github.com/huhen/lampa-web-builder) качает upstream, применяет патчи и overlay (они живут там же), подставляет домен и отдаёт сборку tar.gz по API.

Воркер lampa-go (`internal/builder`) периодически сверяет `available_commit` билдера со своим `deployed_commit` (таблица `app_meta`): при отличии заказывает сборку `{server.base_domain}`, ждёт готовности, скачивает архив и раскладывает его в `<корень static_dir>/versions/<commit>/`, атомарно переключая symlink `current`. На диске хранится `builder.keep_versions` последних версий.
```

6. В §7 (конфиг-пример): `static_dir: ./deploy/web` → `static_dir: ./deploy/web/current`; после блока `cub:` добавить:

```yaml
builder:
  url: http://builder:8080           # lampa-web-builder API
  api_key: <ключ>                    # обязателен при builder.enabled (по умолчанию true)
```

7. В §10 строку «- Скрипт обновления — `fe-diff` (dry-run) + ручная проверка первого реального обновления.» заменить на «- Воркер билдера — тесты с `httptest`-фейком билдера (`internal/builder`).».
8. В §11 удалить строку «- `docs/frontend-update.md` — цикл обновления, создание патчей, разрешение конфликтов;».
9. В §13 абзац про платформенные сборки: «общие патчи `src/` действуют на все платформы, в пайплайн обновления добавляются gulp-таски» → «общие патчи `src/` (репозиторий билдера) действуют на все платформы, в пайплайн билдера добавляются gulp-таски».

- [ ] **Step 2: Проверить, что в old spec не осталось старых ссылок**

Run: `grep -n 'frontend/\|fe-update\|fe-diff\|fe-build\|fe-deploy\|fe-new-patch\|update-frontend\|frontend-update' docs/superpowers/specs/2026-10-07-lampa-go-design.md; echo "exit=$?"`
Expected: `exit=1`.

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/specs/2026-10-07-lampa-go-design.md
git commit -m "docs: спек первой итерации — builder-механизм вместо скрипта фронта"
```

---

### Task 8: Dockerfile + .dockerignore

**Files:**
- Create: `Dockerfile`
- Create: `.dockerignore`

- [ ] **Step 1: Создать Dockerfile**

```dockerfile
# syntax=docker/dockerfile:1

# Stage 1: build the static server binary. The sqlite driver is pure Go
# (modernc), so CGO stays off and the binary is fully static.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/lampa-go ./cmd/lampa-go
# Runtime tree owned by the distroless nonroot user (uid/gid 65532): a fresh
# named volume mounted at /var/lib/lampa-go inherits the owner from the image.
RUN mkdir -p /out/rootfs/var/lib/lampa-go/web && chown -R 65532:65532 /out/rootfs/var

# Stage 2: minimal static runtime.
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/lampa-go /lampa-go
COPY --from=build /out/rootfs/ /
EXPOSE 8080
USER 65532:65532
ENTRYPOINT ["/lampa-go", "-config", "/etc/lampa-go/config.yaml"]
```

- [ ] **Step 2: Создать .dockerignore**

```
.git
.idea
.github
bin/
data/
deploy/
docs/
*.db
*.db-wal
*.db-shm
config.yaml
.env
```

- [ ] **Step 3: Собрать образ**

Run: `docker build -t lampa-go:local .`
Expected: образ собран.

- [ ] **Step 4: Проверить nonroot и запуск**

```bash
docker inspect -f '{{.Config.User}}' lampa-go:local   # ожидаем 65532:65532
dir=$(mktemp -d)
cat > "$dir/config.yaml" <<'EOF'
server:
  listen: ":8080"
  static_dir: /var/lib/lampa-go/web/current
builder:
  enabled: false
db:
  dsn: /var/lib/lampa-go/lampa-go.db
EOF
docker run --rm -d --name lampa-go-local \
  -v "$dir/config.yaml:/etc/lampa-go/config.yaml:ro" \
  -p 127.0.0.1:18081:8080 lampa-go:local
sleep 1
curl -sf http://127.0.0.1:18081/healthz && echo OK
docker rm -f lampa-go-local
```

Expected: `65532:65532`, затем `ok` (healthz) и `OK`.

- [ ] **Step 5: Commit**

```bash
git add Dockerfile .dockerignore
git commit -m "build: Dockerfile (distroless, nonroot) + .dockerignore"
```

---

### Task 9: docker-compose — полный стек

**Files:**
- Modify: `docker-compose.yml` (полная замена)
- Modify: `.env.example`

- [ ] **Step 1: Новое содержимое docker-compose.yml**

```yaml
# Полный стек: билдер фронта + lampa-go
# (спек: docs/superpowers/specs/2026-10-30-builder-only-frontend-design.md).
# Ключи — в .env (шаблон .env.example); конфиг lampa-go — ./config.yaml
# (шаблон config.example.yaml; контейнерные пути — docs/deploy.md, раздел compose).
services:
  builder:
    image: ghcr.io/huhen/lampa-web-builder:latest
    restart: unless-stopped
    volumes:
      - builder-data:/data
    environment:
      LISTEN: ":8080"
      API_KEY: ${BUILDER_API_KEY:?set BUILDER_API_KEY in .env}
      DEFAULT_DOMAIN: ${BUILDER_DEFAULT_DOMAIN:?set BUILDER_DEFAULT_DOMAIN in .env}
      POLL_INTERVAL: "6h"
    # Порт наружу не публикуется: builder доступен только внутри сети compose
    # по имени сервиса.
  lampa-go:
    image: ghcr.io/huhen/lampa-go:latest
    restart: unless-stopped
    depends_on:
      - builder
    ports:
      - "127.0.0.1:8080:8080"   # за reverse proxy
    volumes:
      - ./config.yaml:/etc/lampa-go/config.yaml:ro
      - lampa-data:/var/lib/lampa-go
volumes:
  builder-data:
  lampa-data:
```

- [ ] **Step 2: Обновить .env.example**

Полное содержимое:

```
# cp .env.example .env and fill in
# Must equal lampa-go's config.yaml builder.api_key.
BUILDER_API_KEY=change-me-long-random-string
# Must equal lampa-go's server.base_domain: the builder test-builds this
# domain; a mismatch wastes a rebuild on every update.
BUILDER_DEFAULT_DOMAIN=lampa.example.com
```

- [ ] **Step 3: Валидация compose**

```bash
cp .env.example .env 2>/dev/null || true
docker compose config -q && echo COMPOSE_OK
```

Expected: `COMPOSE_OK` (синтаксис и переменные разрешены). Если `.env` уже существует с реальными ключами — не перезаписывать.

- [ ] **Step 4: Commit**

```bash
git add docker-compose.yml .env.example
git commit -m "build: docker-compose — полный стек builder + lampa-go"
```

---

### Task 10: CI-workflow

**Files:**
- Create: `.github/workflows/ci.yml`

- [ ] **Step 1: Создать workflow**

Образец — workflow lampa-web-builder; проверки адаптированы под distroless (в образе нет `sh`/`id`, пользователя проверяем через `docker inspect`, владельца volume — из контейнера alpine):

```yaml
name: ci

on:
  pull_request:
  push:
    branches: [main]
    tags: ['v*']

# A new push to a PR branch supersedes the previous run of the same branch;
# main and tag runs are never cancelled.
concurrency:
  group: ${{ github.workflow }}-${{ github.head_ref || github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v5
      - uses: actions/setup-go@v6
        with:
          go-version-file: go.mod
      - run: go vet ./...
      - run: go test ./...

  # The distroless image has no shell: the runtime user is asserted via
  # docker inspect, volume ownership — from a side alpine container.
  image-smoke:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v5
      - name: Build the image
        run: docker build -t lampa-go:ci .
      - name: Image runs as nonroot
        run: |
          set -euo pipefail
          user=$(docker inspect -f '{{.Config.User}}' lampa-go:ci)
          echo "user=$user"
          test "$user" = "65532:65532"
      - name: Server starts, answers /healthz, owns the volume
        run: |
          set -euo pipefail
          dir=$(mktemp -d)
          printf 'server:\n  listen: ":8080"\n  static_dir: /var/lib/lampa-go/web/current\nbuilder:\n  enabled: false\ndb:\n  dsn: /var/lib/lampa-go/lampa-go.db\n' > "$dir/config.yaml"
          docker volume create lg-ci
          trap 'docker rm -f lg-ci-run >/dev/null 2>&1 || true; docker volume rm lg-ci >/dev/null 2>&1 || true' EXIT
          docker run --rm -d --name lg-ci-run \
            -v lg-ci:/var/lib/lampa-go \
            -v "$dir/config.yaml:/etc/lampa-go/config.yaml:ro" \
            -p 127.0.0.1:18081:8080 lampa-go:ci
          curl -sf --retry 10 --retry-connrefused --retry-delay 1 http://127.0.0.1:18081/healthz
          # A fresh named volume inherits the owner of the image directory.
          owner=$(docker run --rm -v lg-ci:/vol alpine stat -c %u /vol)
          test "$owner" = 65532

  # Every merge to main publishes an image: main (moving) and sha-<short>
  # (pinned). Release tags keep version tags and latest.
  docker-main:
    if: github.ref == 'refs/heads/main'
    needs: [test, image-smoke]
    runs-on: ubuntu-24.04
    permissions:
      contents: read
      packages: write
    steps:
      - uses: actions/checkout@v5
      - uses: docker/login-action@v4
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: docker/metadata-action@v6
        id: meta
        with:
          images: ghcr.io/huhen/lampa-go
          tags: |
            type=raw,value=main
            type=sha
      - uses: docker/build-push-action@v7
        with:
          context: .
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}

  publish:
    if: startsWith(github.ref, 'refs/tags/v')
    needs: [test, image-smoke]
    runs-on: ubuntu-24.04
    permissions:
      contents: read
      packages: write
    steps:
      - uses: actions/checkout@v5
      - uses: docker/login-action@v4
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: docker/metadata-action@v6
        id: meta
        with:
          images: ghcr.io/huhen/lampa-go
      - uses: docker/build-push-action@v7
        with:
          context: .
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
```

- [ ] **Step 2: Проверить YAML**

Run: `python3 -I -c "import yaml,sys; yaml.safe_load(open('.github/workflows/ci.yml')); print('YAML_OK')"`
Expected: `YAML_OK`.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: тесты, image-smoke и публикация образа в GHCR"
```

---

### Task 11: Финальная проверка и push

- [ ] **Step 1: Полный прогон**

```bash
go vet ./... && go test ./... && make smoke
```

Expected: vet молчит, тесты `ok`, smoke зелёный.

- [ ] **Step 2: Финальный grep — следы старого пайплайна вычищены**

```bash
grep -rn --include='*.go' --include='*.md' --include='*.sh' --include='Makefile' --include='*.yaml' --include='*.yml' \
  'frontend/\|fe-update\|fe-diff\|fe-build\|fe-deploy\|fe-new-patch\|update-frontend\|ORIGIN_COMMIT' \
  . | grep -v 'docs/superpowers/' ; echo "exit=$?"
```

Expected: `exit=1` — в рабочем дереве (кроме архивных материалов `docs/superpowers/`, где старые упоминания допустимы в исторических спеках и этом плане) следов старого пайплайна не осталось. Если grep что-то нашёл — исправить и закоммитить (`docs: вычистить остатки fe-пайплайна`).

- [ ] **Step 3: Push в origin/main**

```bash
git push origin main
```

Expected: коммиты в `main`; на GitHub должен запуститься workflow `ci` (jobs `test`, `image-smoke`; `docker-main` опубликует образ `ghcr.io/huhen/lampa-go:main` и `sha-<sha>`).

- [ ] **Step 4: Проверить CI**

Run: `gh run list --limit 3` (дождаться завершения) и `gh run watch <id>` по желанию.
Expected: `test`, `image-smoke`, `docker-main` — success.
