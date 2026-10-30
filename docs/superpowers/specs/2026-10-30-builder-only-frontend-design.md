# Переход на lampa-web-builder как единственный источник фронтенда

Дата: 2026-10-30. Статус: согласован.

## 1. Цель

lampa-go получает веб-интерфейс **только** через API сервиса
[lampa-web-builder](https://github.com/huhen/lampa-web-builder). Локальный
пайплайн сборки фронта (патчи, overlay, gulp, `make fe-*`) из репозитория
удаляется полностью — он уже перенесён в репозиторий билдера (проверено:
патч `010-gulp-build-task.patch` и overlay `modification.js` идентичны,
домен инжектится там же патчем `020-cub-domains.patch` через `{{CUB_DOMAIN}}`).

Дополнительно в скоупе: контейнеризация lampa-go (Dockerfile + публикация
образа в GHCR) и полный стек `builder + lampa-go` в docker-compose.

История git репозитория намеренно очищена, чтобы убрать ссылки на старый
механизм; всё, что ещё ссылается на старый подход, вычищается этим шагом.

## 2. Текущее состояние

- Интеграция с билдером уже реализована: пакет `internal/builder`
  (клиент API, `Deployer` — versioned-layout `versions/<commit>` + атомарный
  symlink-swap, `Worker` — reconcile `available_commit` ↔ `deployed_commit`),
  секция `builder` в конфиге, wiring в `main.go`. Контракт API — spec билдера §3.
- Старый пайплайн ещё живёт в дереве: `frontend/`, `scripts/update-frontend.sh`,
  цели `fe-*` в Makefile, `docs/frontend-update.md`, упоминания в README,
  `docs/deploy.md`, `docs/architecture.md` и старом спеке.
- `builder.enabled` по умолчанию `false`.

## 3. Удаление старого пайплайна

Удаляется целиком:

- `frontend/` — патч `010`, `overlay/`, `package-lock.json`, `ORIGIN_COMMIT`
  (дубликаты в билдере идентичны); локальный `frontend/sources/` удаляется с диска;
- `scripts/update-frontend.sh`;
- цели `fe-diff`, `fe-update`, `fe-build`, `fe-deploy`, `fe-new-patch` и
  `export FE_DEPLOY_DIR` из `Makefile`;
- `docs/frontend-update.md`;
- `.gitignore`: строки `/frontend/sources/` и `frontend/package-lock.json.bak`;
- `docs/superpowers/plans/` — оба плана (`2026-10-07-lampa-go-first-iteration.md`,
  `2026-10-07-builder-integration.md`): они описывают удалённый пайплайн и
  ссылаются на стёртые ветки/issues;
- в старом спеке `docs/superpowers/specs/2026-10-07-lampa-go-design.md` —
  раздел «Скрипт обновления фронта» и упоминания `frontend/` / `fe-*`
  (структура каталогов, таблица решений): заменяются кратким описанием
  builder-механизма со ссылкой на репозиторий билдера.

Каталог `deploy/web/` (плоская раскладка, gitignored) после миграции не нужен:
воркер создаёт собственный versioned-layout.

## 4. Конфиг

- `Defaults()`: `Builder.Enabled = true` — билдер единственный механизм,
  воркер включён по умолчанию; `Server.StaticDir` → `./deploy/web/current`
  (symlink-путь; `Root` = `./deploy/web`, версии — `./deploy/web/versions`).
- `Validate()` при `enabled` требует непустые `builder.api_key` и
  `server.base_domain` (уже реализовано); боевой конфиг без них не стартует —
  это осознанно. Локальные прогоны без билдера (smoke, тесты) явно пишут
  `builder.enabled: false`.
- `config.example.yaml`: `enabled: true`, `static_dir: ./deploy/web/current`,
  комментарий про compose-имя сервиса (`url: http://builder:8080`).
- Тесты `internal/config` переподписываются под новые дефолты.

## 5. Код

`internal/builder` и `cmd/lampa-go/main.go` не меняются — интеграция полная.
Правки:

- `internal/config` — дефолты (п. 4);
- `scripts/smoke.sh` — временный конфиг дописывает `builder: {enabled: false}`;
- `README.md` — убрать `fe-*`, `frontend/` и заметку про будущий патч
  `manifest.js` (домен инжектит билдер); описать builder-механизм и
  compose-quickstart;
- `docs/architecture.md` — «Попадание фронтенда под /cub/»: патчи и overlay
  живут в репозитории билдера; заметка о синхронизации списков маркеров в
  `modification.js` перенаправляет в репозиторий билдера;
- `docs/deploy.md` — раздел «Обновление фронта через билдер» остаётся
  источником прав, плоская раскладка и `make fe-update` из него уходят.

## 6. Dockerfile

Multi-stage, по образцу билдера:

- **build**: `golang:1.27`, `CGO_ENABLED=0` (sqlite — `modernc.org/sqlite`,
  чистый Go), `-trimpath -ldflags="-s -w"`;
- **runtime**: `gcr.io/distroless/static:nonroot` (uid 65532). Каталоги
  `/var/lib/lampa-go` (БД) и `/var/lib/lampa-go/web` (Root статики) создаются
  в build-стейдже и копируются с `--chown=nonroot:nonroot`, чтобы свежий named
  volume унаследовал владельца;
- `EXPOSE 8080`,
  `ENTRYPOINT ["/lampa-go", "-config", "/etc/lampa-go/config.yaml"]`.

Пути в контейнере: БД `/var/lib/lampa-go/lampa-go.db`,
`static_dir: /var/lib/lampa-go/web/current` — один volume `lampa-data`
покрывает оба. Файл `.dockerignore` (bin, data, deploy, .git, .idea, docs).

ENV-оверрайдов конфига не заводим: `config.yaml` монтируется read-only,
секрет `builder.api_key` живёт в нём.

## 7. docker-compose

Полный стек в одном файле:

```yaml
services:
  builder:   # без published-порта — доступен только по имени сервиса
    image: ghcr.io/huhen/lampa-web-builder:latest
    ...
  lampa-go:
    image: ghcr.io/huhen/lampa-go:latest
    restart: unless-stopped
    depends_on: [builder]
    ports: ["127.0.0.1:8080:8080"]   # за reverse proxy
    volumes:
      - ./config.yaml:/etc/lampa-go/config.yaml:ro
      - lampa-data:/var/lib/lampa-go
volumes:
  builder-data:
  lampa-data:
```

Сеть — общая default; `builder.url: http://builder:8080`.
`BUILDER_API_KEY`/`BUILDER_DEFAULT_DOMAIN` — в `.env` для builder;
для lampa-go тот же ключ в `config.yaml`. Комментарий-переходник
«пока lampa-go не контейнеризован» из compose уходит, `.env.example`
обновляется.

## 8. CI

`.github/workflows/ci.yml` зеркально билдеру:

- `test`: `go vet ./...` + `go test ./...` на PR и push;
- `image-smoke` (push main/tags): `docker build`; проверка nonroot-uid;
  владение volume; запуск контейнера с минимальным конфигом
  (`builder.enabled: false`) и `curl /healthz`;
- `publish` (push main/tags): `docker/metadata-action` + `build-push-action`
  в `ghcr.io/huhen/lampa-go` (`sha-<sha>` с main, `latest` с тегов `v*`).

## 9. Тесты и проверки

- `go test ./...` и `go vet ./...` — зелёные после правок;
- `make smoke` — проходит с `builder.enabled: false` без изменений прочих проверок;
- боевой прогон (`docker compose up` с реальным билдером, фронт появляется сам)
  отложен до боевого домена.

## 10. Рабочий процесс

Первичная разработка — в ветке `main` (до первого минимального результата и
удаления старых хвостов), далее — обычные правила (push + PR).
Первый коммит очищенной истории — только этот документ; старый пайплайн в
историю не попадает вообще.

## 11. Вне скоупа

- ENV-оверрайды конфигурации;
- платформенные сборки (webOS/Tizen) — зона ответственности билдера;
- reverse proxy и TLS — боевой домен ещё не выбран.
