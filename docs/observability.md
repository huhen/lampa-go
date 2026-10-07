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

Важно про инжест: VictoriaMetrics принимает OTLP **по HTTP** (endpoints `/opentelemetry/v1/metrics` и т.д.). OTLP **gRPC** (`:4317`) — это вход для фронта приёма: OTel Collector или vmagent. Наша схема:

```
lampa-go --OTLP gRPC :4317--> collector / vmagent --OTLP HTTP--> VictoriaMetrics
```

Метрики приходят в VictoriaMetrics; трейсы уходят в отдельное хранилище (VictoriaTraces), логи — в VictoriaLogs (см. ниже).

Конфигурация lampa-go:

```yaml
otel:
  enable: true
  endpoint: collector.internal:4317   # строго host:port, OTLP gRPC
  service_name: lampa-go
  insecure: true                      # TLS терминируется на collector'е
```

Пример конфигурации OTel Collector (`otelcol-config.yaml`), перекладывающего все три сигнала в VictoriaMetrics по OTLP HTTP:

```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
exporters:
  otlphttp:
    endpoint: http://victoriametrics:8428/opentelemetry   # + /v1/metrics, /v1/traces
services:
  pipelines:
    traces:  { receivers: [otlp], exporters: [otlphttp] }
    metrics: { receivers: [otlp], exporters: [otlphttp] }
```

Вместо Collector можно поставить vmagent с OTLP-приёмом (фронт `:4317` и `--remoteWrite.url` в VictoriaMetrics — см. документацию vmagent).

**Логи** — [VictoriaLogs](https://docs.victoriametrics.com/victorialogs/): принимает OTLP по HTTP (`/opentelemetry/v1/logs`); достаточно добавить в Collector pipeline `logs` с отдельным экспортёром на endpoint VictoriaLogs.

**Ошибки самого OTel SDK** (недоступный endpoint, ошибки экспорта) пишутся специальным stdout-only логгером — они **не** заводятся обратно в `core.Logger`, чтобы не ре-энтерить пайплайн экспорта логов (иначе ошибка отправки логов породила бы новую запись лога и так по кругу).

## Выключенный режим

`otel.enable: false` (по умолчанию): устанавливаются noop-провайдеры tracer/meter, логи идут только в stdout. Нулевой оверхед: span'ы не создают телеметрии, счётчики и гистограммы не пишутся. `main` в любом случае делает `core.Shutdown` (бюджет 5s) — в выключенном режиме это мгновенный no-op.
