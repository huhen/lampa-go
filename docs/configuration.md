# Конфигурация

Конфиг — YAML-файл, путь задаётся флагом `-config` (см. `internal/config`). Пример с пояснениями — `config.example.yaml`; скопируйте его в `config.yaml` и поправьте под себя.

Все ключи имеют встроенные значения по умолчанию, кроме тех, что помечены в таблице как обязательные при включении соответствующей функциональности.

## Ключи

### server

| Ключ | Тип | Default | Описание |
|---|---|---|---|
| `server.listen` | string | `:8080` | адрес прослушивания; за reverse-proxy обычно `127.0.0.1:8080` |
| `server.static_dir` | string | `./deploy/web` | каталог с собранным фронтом (результат `make fe-update`); создаётся при старте, если нет |
| `server.base_domain` | string | *(пусто)* | наш публичный домен; в первой итерации ни на что не влияет — зарезервирован под будущий frontend-патч |

### cub

| Ключ | Тип | Default | Описание |
|---|---|---|---|
| `cub.upstream` | string | см. `internal/config` | upstream для `/cub/*`; схема строго переносится в запрос, хост может заменяться subdomain-маркером (`tmdb.<host>`). Для продакшена — без порта (или `:443`), см. [deploy.md](deploy.md) |
| `cub.timeout` | duration | `15s` | таймаут всего обмена с upstream, включая прокачку response body; должен быть > 0 |
| `cub.geo_header` | string | `X-Geo-Country` | заголовок, из которого заглушка `/cub/geo` берёт страну клиента (ставит reverse-proxy) |
| `cub.geo_default` | string | `US` | fallback, когда заголовка нет |
| `cub.subdomain_markers` | []string | `["tmdb","geo","ws","imagetmdb","cdn","ad"]` | первый сегмент пути после `/cub/`, превращающий upstream-хост в `<marker>.<upstream-host>` |

### builder

Интеграция с [lampa-web-builder](https://github.com/huhen/lampa-web-builder): lampa-go сам
сверяет свой задеплоенный коммит фронта с `available_commit` билдера, заказывает сборку,
скачивает архив и атомарно выкладывает его (symlink-swap).

| Ключ | По умолчанию | Значение |
|---|---|---|
| `enabled` | `false` | включить воркер обновления фронта |
| `url` | `http://builder:8080` | адрес API билдера; в compose — имя сервиса |
| `api_key` | — | ключ `X-API-Key`; обязателен при `enabled: true` |
| `poll_interval` | `5m` | как часто сверяться с билдером |
| `keep_versions` | `3` | сколько версий хранить в `<корень static_dir>/versions/` |

При `enabled: true` обязательны `server.base_domain` — им становится домен заказываемой
сборки. `server.static_dir` должен указывать на путь **symlink'а** (например,
`./deploy/web/current`): билдер-воркер кладёт версии в `<корень>/versions/<commit>/` и
атомарно переименовывает symlink; если `static_dir` — обычный каталог, старт падает с
подсказкой (см. deploy.md про миграцию).

### db

| Ключ | Тип | Default | Описание |
|---|---|---|---|
| `db.driver` | string | `sqlite` | `sqlite` или `postgres` |
| `db.dsn` | string | `./data/lampa-go.db` | путь к файлу SQLite или DSN PostgreSQL (`postgres://user:pass@host/lampa`); каталог для SQLite создаётся автоматически |
| `db.max_open_conns` | int | `25` | размер пула; **игнорируется для SQLite** (форсируется `1`, single writer); не должен быть отрицательным |
| `db.max_idle_conns` | int | `5` | 0 = не держать idle-соединений (стандартная семантика `database/sql`); не должен быть отрицательным |
| `db.conn_max_lifetime` | duration | `30m` | время жизни соединения; 0 = переиспользовать вечно; не должно быть отрицательным |

### otel

| Ключ | Тип | Default | Описание |
|---|---|---|---|
| `otel.enable` | bool | `false` | `false` → noop-провайдеры, логи только в stdout |
| `otel.endpoint` | string | `localhost:4317` | OTLP gRPC endpoint — **строго `host:port`**, со схемой (`://`) конфиг невалиден; обязателен при `enable: true` |
| `otel.service_name` | string | `lampa-go` | `service.name` в OTel resource; обязателен при `enable: true` |
| `otel.insecure` | bool | `true` | без TLS на OTLP-соединении |

### log

| Ключ | Тип | Default | Описание |
|---|---|---|---|
| `log.level` | string | `info` | `debug` \| `info` \| `warn` \| `error` (парсится `slog.Level`) |
| `log.format` | string | `text` | `text` \| `json` |

## Строгий разбор

Неизвестные ключи → **ошибка старта** (`KnownFields(true)` у yaml-декодера): опечатка вроде `staticdir` не будет молча проигнорирована, процесс не поднимется. Пустой файл или файл только из комментариев ошибкой не считается — работают дефолты.

## Duration-поля

`cub.timeout` и `db.conn_max_lifetime` парсятся `time.ParseDuration`: `"15s"`, `"1m30s"`, `"500ms"`. Значение должно быть строкой; `cub.timeout` обязан быть положительным (в `net/http` неположительный таймаут означает «нет таймаута», чего мы сознательно не допускаем), `conn_max_lifetime` — неотрицательным.

## SQLite

- single writer: `SetMaxOpenConns(1)`, `db.max_open_conns` игнорируется;
- файл — обычный путь в `db.dsn` (понимается и префикс `file:`, и query-часть);
- pragmas (`journal_mode=WAL`, `foreign_keys=1`, `busy_timeout=5000`) передаются как DSN-параметры и применяются на **каждом** соединении пула — `PRAGMA` в SQLite connection-scoped, а `database/sql` может подменить соединение;
- side-файлы `*.db-wal` / `*.db-shm` рядом с основным (в `.gitignore` уже внесены).

## PostgreSQL

- драйвер — `pgx/v5/stdlib` поверх `database/sql`; пул — сам `database/sql`;
- `max_open_conns` / `max_idle_conns` / `conn_max_lifetime` применяются как есть, с 0-семантикой `database/sql`: `max_idle_conns: 0` = не держать idle-соединений, `conn_max_lifetime: 0s` = соединения живут вечно; `max_open_conns: 0` = без ограничения (не рекомендуется);
- миграции goose те же, диалект `postgres`.

## OTel

`otel.enable: true` требует заполненных `endpoint` и `service_name` — иначе ошибка валидации на старте. `endpoint` — строго `host:port` для OTLP gRPC (значение со схемой отвергается). Все эти проверки выполняются **только при `otel.enable: true`**: при `enable: false` конфигурация OTel не валидируется и не используется вовсе (noop-провайдеры). `insecure: true` отключает TLS на соединении с коллектором. Подробности и схема с VictoriaMetrics — [observability.md](observability.md).

## Log

`log.level`: `debug` | `info` | `warn` | `error`. `log.format`: `text` | `json` — формат stdout-логгера `log/slog`. При включённом OTel логи пишутся и в stdout, и в OTLP-экспортёр (multi-handler), при выключенном — только в stdout.

## Флаг -config

`lampa-go -config config.yaml`. Пустой флаг (по умолчанию) → конфиг не читается вообще, работает **полностью дефолтная** конфигурация. Несуществующий путь или невалидный YAML → ошибка и exit code 1.
