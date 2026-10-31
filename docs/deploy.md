# Deploy

## Схема

```
browser --HTTPS--> reverse-proxy (TLS) --HTTP--> lampa-go (127.0.0.1:8080)
```

lampa-go слушает plain HTTP (по умолчанию `:8080`; в продакшене лучше `127.0.0.1:8080`, чтобы порт не был доступен снаружи). TLS терминируется на reverse-proxy. Про заголовок страны для `/cub/geo` — ниже.

## Caddy

```caddyfile
lampa.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

TLS-сертификаты Caddy получает сам. Если нужен `/cub/geo` по реальной стране клиента, добавьте `header_up X-Geo-Country <значение>` (например, через плагин geoip).

## nginx

```nginx
server {
    listen 443 ssl;
    server_name lampa.example.com;
    # ssl_certificate ...; ssl_certificate_key ...;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;

        # опционально: страна клиента для заглушки /cub/geo
        # (модуль ngx_http_geoip2)
        # geoip2 /usr/share/GeoIP/GeoLite2-Country.mmdb {
        #     $geoip2_data_country_code country iso_code;
        # }
        # proxy_set_header X-Geo-Country $geoip2_data_country_code;
    }
}
```

Имя заголовка настраивается на нашей стороне — `cub.geo_header` (по умолчанию `X-Geo-Country`), при отсутствии заголовка заглушка отдаёт `cub.geo_default` (`US`).

## Важные примечания

### 1. X-Forwarded-Proto за TLS-фронтом

Факт (см. `internal/cubproxy` + stdlib `httputil`): перед `Rewrite` ReverseProxy удаляет клиентские `Forwarded` и `X-Forwarded-For/Host/Proto` (anti-spoof), а `SetXForwarded()` затем выставляет заголовки заново:

- `X-Forwarded-For` — адрес непосредственного пира (для lampa-go это reverse-proxy);
- `X-Forwarded-Host` — клиентский `Host`;
- `X-Forwarded-Proto` — по признаку `In.TLS`, т.е. **только если lampa-go сам терминирует TLS**.

Поскольку lampa-go принимает plain HTTP, `In.TLS` всегда nil, и к cub-upstream уходит `X-Forwarded-Proto: http` — **даже если reverse-proxy поставил свой `X-Forwarded-Proto: https`**: `SetXForwarded()` перезаписывает заголовок безусловно, cubproxy его не пробрасывает. Если upstream'у важен реальный протокол клиента, это надо решать на стороне upstream (например, игнорировать `X-Forwarded-Proto`) — заголовком от reverse-proxy ситуацию не починить.

### 2. marker + upstream с портом

Subdomain-marker приклеивается к `upstream.Host` **как есть, вместе с портом**: при `upstream: http://localhost:18092` запрос `/cub/tmdb/3/movie/1` уйдёт на `tmdb.localhost:18092`. Для локальной разработки и smoke-теста это удобно; в продакшене upstream должен быть без порта (или `:443`), чтобы `tmdb.<host>` резолвился на стандартный HTTPS-порт.

### 3. systemd unit

```ini
[Unit]
Description=lampa-go (Lampa web UI + cub API)
After=network.target

[Service]
Type=simple
WorkingDirectory=/opt/lampa-go
ExecStart=/opt/lampa-go/bin/lampa-go -config /opt/lampa-go/config.yaml
Restart=on-failure
# Graceful shutdown должен уложиться: до 10s drain соединений + 5s flush телеметрии.
# Меньший TimeoutStopSec -> systemd пошлёт SIGKILL посреди flush экспортёров.
TimeoutStopSec=20

[Install]
WantedBy=multi-user.target
```

Примечания:

- `Type=simple`, сигнал остановки — `SIGTERM`; по нему выполняется graceful shutdown: до 10 секунд на drain активных запросов (`srv.Shutdown`), затем до 5 секунд на flush телеметрии (`core.Shutdown`). Отсюда требование **`TimeoutStopSec=15+`** — иначе systemd прибьёт процесс SIGKILL'ом посреди flush.
- Если drain не уложился в 10s (долгие стримы), `main` возвращает ошибку graceful shutdown и процесс завершается с **exit code 1**; при `Restart=on-failure` сервис поднимется заново.
- Файл БД (`db.dsn`) и каталог статики — относительные пути по умолчанию, поэтому `WorkingDirectory` важен; либо укажите абсолютные пути в `config.yaml`.

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

## Обновление фронта через билдер

При `builder.enabled: true` фронтенд обновляется автоматически из
[lampa-web-builder](https://github.com/huhen/lampa-web-builder): воркер lampa-go сверяет
`deployed_commit` (таблица `app_meta`) с `available_commit` билдера, при отличии заказывает
сборку домена `server.base_domain`, скачивает tar.gz и выкладывает атомарно.
`BUILDER_DEFAULT_DOMAIN` в `.env` билдера должен совпадать с `server.base_domain` lampa-go:
билдер прогоняет тестовые сборки на этом домене, и расхождение означает бесполезную
пересборку при каждом обновлении.

Раскладка каталогов (`<корень>` — каталог, в котором лежит `static_dir`):

    <корень>/versions/<commit>/   распакованные сборки
    <корень>/current              symlink → versions/<commit>; путь из server.static_dir

Подмена — rename свежесозданного symlink'а поверх `current`: атомарная операция, окно 404
отсутствует (закрывает issue #7). Хранятся последние `builder.keep_versions` версий.

### compose

`compose.yaml` в корне репозитория поднимает полный стек: `builder`
(порт наружу не публикуется, доступен по имени сервиса `builder`) и `lampa-go`
(публикуется на `LAMPA_GO_ADDR` из `.env`, по умолчанию `0.0.0.0:3080` —
reverse-proxy на отдельной машине). Ключи — в `.env` (шаблон `.env.example`):
`BUILDER_API_KEY` должен совпадать с `builder.api_key` в `config.yaml`,
`BUILDER_DEFAULT_DOMAIN` — с `server.base_domain`. Конфиг монтируется
read-only, данные живут в bind-каталогах рядом с compose-файлом:
`./data/builder` (данные билдера) и `./data/lampa-go` (БД + версии фронта);
каталоги должны быть доступны на запись uid контейнеров (10001 / 65532).

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
