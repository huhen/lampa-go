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
