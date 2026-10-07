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

Фронтенд:

```bash
make fe-update      # новый upstream-коммит -> патчи -> сборка -> deploy/web
```

Рестарт lampa-go **не требуется**: статика читается с диска на каждый запрос, а деплой подменяет каталог rename'ами (сначала готовый `<deploy>.tmp`, затем текущий → `<deploy>.old`, `tmp` → deploy, `old` удаляется). Каталог не бывает недописанным (rename атомарен); между двумя rename существует микроскопический интервал без каталога — in-flight запрос может получить 404; при сбое второго `mv` каталог восстанавливается из `.old` вручную. `index.html` и `assembly.json` отдаются с `Cache-Control: no-cache`, остальная статика — на сутки.

Go-код:

```bash
make build
systemctl restart lampa-go
```

`systemctl restart` шлёт `SIGTERM` — срабатывает штатный graceful shutdown (см. unit выше).

## Обновление фронта через билдер

При `builder.enabled: true` фронтенд обновляется автоматически из
[lampa-web-builder](https://github.com/huhen/lampa-web-builder): воркер lampa-go сверяет
`deployed_commit` (таблица `app_meta`) с `available_commit` билдера, при отличии заказывает
сборку домена `server.base_domain`, скачивает tar.gz и выкладывает атомарно.

Раскладка каталогов (`<корень>` — каталог, в котором лежит `static_dir`):

    <корень>/versions/<commit>/   распакованные сборки
    <корень>/current              symlink → versions/<commit>; путь из server.static_dir

Подмена — rename свежесозданного symlink'а поверх `current`: атомарная операция, окно 404
отсутствует (закрывает issue #7). Хранятся последние `builder.keep_versions` версий.

### compose

`docker-compose.yml` в корне репозитория поднимает билдер; ключи — в `.env`
(шаблон `.env.example`). Пока lampa-go работает бинарником на хосте, раскомментируйте
loopback-публикацию порта и укажите `builder.url: http://127.0.0.1:8081`; после
контейнеризации lampa-go — `builder.url: http://builder:8080` (общая сеть compose,
порты наружу не публикуются).

### Миграция с плоской раскладки

Старая раскладка (файлы прямо в `static_dir`) с билдером не работает — `static_dir` должен
указывать на symlink. Переезд:

1. Остановить lampa-go.
2. Перенести текущую статику в версионный каталог (`<commit>` — версия текущей сборки,
   например `b4a13b6…`; если неизвестна — любой маркер, первый цикл воркера всё равно
   задеплоит свежую версию):

   ```bash
   cd deploy/web
   mkdir -p versions/<commit>
   # всё верхнего уровня, кроме versions/, уезжает в версионный каталог
   find . -mindepth 1 -maxdepth 1 ! -name versions -exec mv {} versions/<commit>/ \;
   ln -s versions/<commit> .current-tmp && mv -T .current-tmp current
   ```

3. В конфиге: `server.static_dir: ./deploy/web/current`, секция `builder`.
4. Запустить lampa-go. Пустой `deployed_commit` означает «обновиться до доступного»:
   первый цикл воркера сам задеплоит актуальную версию из билдера — ручной сеанс коммита
   в БД не нужен.

Проще альтернатива, если старая статика не дорога: удалить `deploy/web` целиком, указать
`static_dir: ./deploy/web/current` и запустить lampa-go — воркер развернёт фронт с нуля.
