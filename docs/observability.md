# Наблюдаемость

Провайдеры OpenTelemetry собираются в `internal/obs`. Один конфиг-блок `otel` управляет всеми тремя сигналами сразу.

## Сигналы

### Traces

- span на каждый HTTP-запрос (`SpanKindServer`), создаётся в middleware `observe`;
- изначально имя — `METHOD /path`, после ответа span **переименовывается по route-паттерну** `http.ServeMux` (например `GET /cub/{rest...}`) — спаны группируются по маршруту, а не по конкретному URL;
- атрибут `http.response.status_code`.

### Metrics

- `http.server.requests` — counter: +1 на запрос;
- `http.server.duration` — гистограмма в **миллисекундах** (`WithUnit("ms")`);
- лейблы обоих инструментов: `http.request.method`, `http.route`, у counter дополнительно `http.response.status_code`. Набор лейблов ограничен паттернами маршрутов — **bounded cardinality**, взрыва меток нет даже на проксируемых путях.

### Logs

- `log/slog`, формат `text` | `json` в stdout (по умолчанию `info`);
- каждый запрос логируется записью `http request` с полями `method`, `path`, `status`, `duration`, а также `route` (если совпал паттерн), `request_id` (см. [architecture.md](architecture.md)) и `trace_id` текущего span — так логи связываются с трейсами;
- при включённом OTel используется multi-handler: запись идёт и в stdout, и в OTLP-логи через мост `otelslog`.

## Подключение к VictoriaMetrics

Важно про инжест: **VictoriaMetrics принимает OTLP только по HTTP** — `/opentelemetry/v1/metrics` (по умолчанию `:8428`; у vmagent тот же путь на своём `-httpListenAddr`, по умолчанию `:8429`). OTLP **gRPC**-приём в экосистеме VictoriaMetrics есть только у **VictoriaTraces**; vmagent gRPC-фронтом не является. Наш сервер экспортирует все три сигнала по OTLP gRPC (`:4317`), поэтому прямой фронт приёма для нас — **OTel Collector** (или другой gRPC-совместимый приёмник); vmagent имеет смысл только как HTTP-ретранслятор **после** collector'а.

```
                       |--> OTLP HTTP --> VictoriaMetrics (метрики) /opentelemetry/v1/metrics
lampa-go --OTLP gRPC--> OTel Collector
                       |--> OTLP HTTP --> VictoriaTraces  (трейсы) /insert/opentelemetry/v1/traces
                       |--> OTLP HTTP --> VictoriaLogs    (логи)   /insert/opentelemetry/v1/logs
```

Конфигурация lampa-go:

```yaml
otel:
  enable: true
  endpoint: collector.internal:4317   # строго host:port, OTLP gRPC
  service_name: lampa-go
  insecure: true                      # TLS терминируется на collector'е
```

Пример конфигурации OTel Collector (`otelcol-config.yaml`): принимает gRPC `:4317` и раскладывает сигналы по трём хранилищам (экспортёр `otlphttp` добавляет к endpoint'у `/v1/metrics`, `/v1/traces` или `/v1/logs`; порты в примере — дефолтные, меняются `-httpListenAddr`):

```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
exporters:
  otlphttp/metrics:
    endpoint: http://victoriametrics:8428/opentelemetry        # -> /opentelemetry/v1/metrics
  otlphttp/traces:
    endpoint: http://victoriatraces:10428/insert/opentelemetry # -> /insert/opentelemetry/v1/traces
  otlphttp/logs:
    endpoint: http://victorialogs:9428/insert/opentelemetry    # -> /insert/opentelemetry/v1/logs
service:
  pipelines:
    metrics: { receivers: [otlp], exporters: [otlphttp/metrics] }
    traces:  { receivers: [otlp], exporters: [otlphttp/traces] }
    logs:    { receivers: [otlp], exporters: [otlphttp/logs] }
```

vmagent вместо VictoriaMetrics в качестве получателя метрик — вариант той же схемы: collector → vmagent по OTLP HTTP → VictoriaMetrics по remote write; сам vmagent нашим OTLP gRPC не принимает.

**Ошибки самого OTel SDK** (недоступный endpoint, ошибки экспорта) пишутся специальным stdout-only логгером — они **не** заводятся обратно в `core.Logger`, чтобы не ре-энтерить пайплайн экспорта логов (иначе ошибка отправки логов породила бы новую запись лога и так по кругу).

## Выключенный режим

`otel.enable: false` (по умолчанию): устанавливаются noop-провайдеры tracer/meter, логи идут только в stdout. Нулевой оверхед: span'ы не создают телеметрии, счётчики и гистограммы не пишутся. `main` в любом случае делает `core.Shutdown` (бюджет 5s) — в выключенном режиме это мгновенный no-op.
