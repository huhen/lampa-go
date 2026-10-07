# lampa-go

## Что это

**lampa-go** — Go-сервер, который хостит веб-интерфейс [Lampa](https://github.com/yumata/lampa-source) и реализует скелет API **cub** по схеме «свой ответ / прокси». Это переработка (порт) C#-проекта [lampac](https://github.com/immisteriou/lampac), который остаётся образцом конечного результата.

Идея — **один домен**: сайт обращается только к нам, больше ничего настраивать на клиентах не нужно. Часть ручек cub API сервер отвечает сам (заглушки: checker, blacklist, metric, ads, geo), остальное прозрачно проксируется на настраиваемый upstream (`cub.upstream`; встроенный дефолт — см. `Defaults()` в `internal/config/config.go`).

## Возможности первой итерации

- **Статика собранного фронта** из `server.static_dir` (по умолчанию `./deploy/web`): `index.html` и `assembly.json` с `Cache-Control: no-cache`, остальное — на сутки; листинг каталогов отключён.
- **Заглушки cub API**: `/cub/api/checker`, `/cub/api/plugins/blacklist`, `/cub/api/metric/...`, `/cub/api/ad/...`, `/cub/geo` — ответы формируются локально.
- **Прокси `/cub/*`**: path-preserving reverse-proxy на `httputil.ReverseProxy` с subdomain markers (`/cub/tmdb/...` → `tmdb.<upstream-host>/...`), фильтрацией response-заголовков и общим таймаутом на обмен.
- **OpenTelemetry**: traces + metrics + logs через OTLP gRPC; по умолчанию выключен (noop-провайдеры).
- **SQLite** (по умолчанию, pure-Go драйвер modernc) **или PostgreSQL** (pgx stdlib) + goose-миграции, embedded в бинарник.
- **Пайплайн обновления фронта** с патч-файлами и overlay (`make fe-update`), см. [docs/frontend-update.md](docs/frontend-update.md).

## Быстрый старт

Требования: **Go 1.27+**, **node 18+**, **npm**, **git** (для пайплайна фронта).

```bash
cp config.example.yaml config.yaml   # при необходимости поправить
make fe-update                       # собрать фронт в deploy/web (клон lampa-source + патчи)
make run                             # = make build && ./bin/lampa-go -config config.yaml
```

Открыть `http://localhost:8080`.

Проверка работоспособности без фронта и внешней сети: `make smoke` (нужен `python3`).

**Примечание про manifest.js.** Пока не создан патч, который прописывает наш домен в `src/core/manifest.js` (будущий `020-*`; `010-*` уже занят gulp-таском), фронт считает зеркалами куба дефолтные `cub_mirrors` из upstream-манифеста — и именно эти запросы плагин `modification.js` (попадает в сборку через overlay) переписывает на лету в `/cub/...` нашего домена. Патч `manifest.js` (`cub_mirrors = ['<боевой домен>']`) сделает наш домен «родным» зеркалом для фронта; процедура создания — в [docs/frontend-update.md](docs/frontend-update.md).

## Структура репозитория

```
cmd/lampa-go/          точка входа: флаги, сигналы, graceful shutdown
internal/server/       сборка HTTP-сервера: маршрутизация и middleware chain
internal/api/          свои ручки: /healthz, /readyz и заглушки cub API
internal/cubproxy/     reverse-proxy /cub/* на upstream
internal/web/          раздача статики (cache-control, без листинга)
internal/storage/      sqlite/postgres + goose-миграции (internal/storage/migrations)
internal/obs/          OpenTelemetry (traces/metrics/logs) + slog
internal/config/       YAML-конфиг: defaults, строгий разбор, валидация
frontend/              пайплайн фронта:
  patches/             патч-файлы NNN-name.patch, применяются по порядку
  overlay/             файлы, копируются поверх sources при каждой сборке
  sources/             pristine-клон lampa-source (в git не входит)
  ORIGIN_COMMIT        коммит последнего применённого апдейта
  package-lock.json    наш pinned lockfile (у upstream его нет)
scripts/               update-frontend.sh (пайплайн фронта), smoke.sh
deploy/web/            собранный фронт — результат fe-update (в git не входит)
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
| `make fe-diff` | сводка изменений upstream с последнего апдейта (без сборки) |
| `make fe-update` | полный цикл: fetch → патчи → overlay → build → deploy |
| `make fe-build` | пересобрать текущий `frontend/sources` + свежий overlay |
| `make fe-deploy` | выложить последнюю сборку в `deploy/web` |
| `make fe-new-patch NAME=NNN-slug` | сохранить правки в `sources` как `frontend/patches/NNN-slug.patch` |

## Документация

- [docs/architecture.md](docs/architecture.md) — маршруты, middleware, прокси, хранилище
- [docs/frontend-update.md](docs/frontend-update.md) — пайплайн обновления фронта и работа с патчами
- [docs/configuration.md](docs/configuration.md) — все ключи конфигурации
- [docs/observability.md](docs/observability.md) — сигналы и подключение к VictoriaMetrics
- [docs/deploy.md](docs/deploy.md) — reverse-proxy, systemd, обновление в бою

## Лицензии

- Фронтенд (репозиторий lampa-source, его сборка попадает в `deploy/web`) распространяется по **GPL-2.0** — см. `LICENSE` в репозитории lampa-source (копия лежит и в самой сборке).
- Лицензия lampa-go в репозитории пока не зафиксирована — вопрос (в том числе совместимости с GPL-2.0 фронта) остаётся за владельцем проекта.
