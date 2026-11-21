# Runbook: observabilidad

Trazas y métricas con OpenTelemetry (OTLP), errores con Sentry, y logs en JSON a stdout.
Todo lo que sale del proceso pasa por el filtro del **ADR 0004**: solo los atributos de
su lista (tenant_id, ruta como patrón, método, estado, código de error, versión, entorno,
servicio externo, resultado y SQL con marcadores). Nada de `user_id`, emails, IPs,
argumentos SQL, cuerpos, cabeceras, query strings ni tokens.

## Qué se ve dónde

| Señal | Dónde | Qué lleva |
|---|---|---|
| Trazas | Tempo (local) / Grafana Cloud (1B) | Un span por petición, llamado por el patrón de `ServeMux` (`GET /tenant/members/{userId}`), con `tenant_id`; debajo, un span por consulta (el nombre de sqlc, `rls.set_vars` para las variables de RLS, o la operación) con el SQL sin argumentos, y los spans de cliente de WorkOS (`workos.*`) y SMTP (`smtp.send`) con su resultado. Sin spans para `/healthz` y `/readyz` |
| Métricas | Prometheus (local) / Grafana Cloud (1B) | HTTP (`http_server_request_duration_seconds` por método, ruta y estado), Postgres (`db_client_operation_duration_seconds` por consulta), pool (`pgxpool_*`), y las propias: `auth_login_total` y `mfa_verify_total` (por `result`), `authz_denied_total` (por `tenant_id`), `auth_tenant_forbidden_total` (sin tenant: es la señal de sondeo), `rate_limit_exceeded_total` (por `http_route`) e `invitation_email_total` (por `result`) |
| Errores (API) | Sentry | Todo error que acaba en 5xx y todo pánico (respuesta 500 Problem Details), con `release` = SHA, `environment` = `APP_ENV` y las etiquetas `tenant_id`, `route` y `trace_id`. Los 4xx esperados nunca |
| Errores (front) | Sentry | Errores de render y de rutas (pantalla de error genérica). Sin Session Replay, sin trazas ni `traceparent` hacia la API, sin source maps (1B) |
| Logs | stdout, JSON | Como siempre (diseño 5.4.4), y ahora con `trace_id` y `span_id` cuando hay span. No se exportan |

El resultado de `auth.login` y `mfa.verify` es `success`, el código de error de la API
(`auth.stale_authentication`, `mfa.code_invalid`…) o `internal`.

## Variables

| Variable | Dónde se lee | Valor |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `config` (decide si hay telemetría) y el exportador | URL base del colector OTLP/HTTP (`http://localhost:4318` en desarrollo). Vacía: sin trazas ni métricas, todo no-op |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`, `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | Igual | Para separar trazas y métricas; con uno solo, solo esa señal |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `config` | Vacía o `http/protobuf`. La API no incluye los exportadores gRPC: con `grpc` no arranca |
| `OTEL_EXPORTER_OTLP_HEADERS` | Solo el exportador | Credenciales del colector (Grafana Cloud: `Authorization=Basic …`). **Es un secreto**: va en el fichero de secretos del entorno (SOPS), nunca en `mise.toml`. La API no lo lee ni lo registra |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | El SDK | Por defecto `parentbased_always_on`. En producción, p. ej. `parentbased_traceidratio` y `0.2` |
| `OTEL_METRIC_EXPORT_INTERVAL` | El SDK | Milisegundos; 60000 por defecto, 10000 en desarrollo |
| `SENTRY_DSN` | `config` (secreto, enmascarado) | DSN del proyecto de la API. Vacía: sin Sentry |
| `VITE_SENTRY_DSN`, `VITE_RELEASE` | Build del front | DSN del proyecto del front y versión (el SHA). Sin DSN, sin Sentry |

Un colector que no responde no frena nada: las trazas salen en lotes y las métricas
periódicamente, en segundo plano, y cada exportación fallida se registra como `Warn`
(`telemetry export failed`). Al apagarse, la API vacía lo pendiente con 5 s como máximo.

## En local

```sh
mise run up          # Postgres, Mailpit y grafana/otel-lgtm
mise run dev         # exporta a http://localhost:4318 (mise.toml)
mise run obs         # Grafana (admin / admin) en el dashboard "Factory Template API"
```

- **Dashboard**: `infra/grafana/dashboards/api.json`, cargado al arrancar el contenedor
  (`infra/grafana/dashboards.yaml`). Si lo cambias, `docker compose up -d
  --force-recreate lgtm` (un reinicio a secas puede chocar con la copia ya provisionada).
- **Una traza**: Explore → Tempo → TraceQL `{name="GET /tenant/members"}`, o la tabla
  "Trazas recientes" del dashboard. Desde un log, busca su `trace_id`.
- **Métricas**: Explore → Prometheus, p. ej. `sum by (http_route)
  (rate(http_server_request_duration_seconds_count[5m]))`.
- El contenedor no guarda datos entre recreaciones.

## Fase 1B: Grafana Cloud y Sentry

1. **Grafana Cloud** (región UE): en el stack, *OpenTelemetry* → *Configure* da el
   endpoint OTLP (`https://otlp-gateway-prod-eu-west-2.grafana.net/otlp` o el que toque) y
   un token. En el entorno:
   - `OTEL_EXPORTER_OTLP_ENDPOINT=<endpoint>`;
   - `OTEL_EXPORTER_OTLP_HEADERS=Authorization=Basic <base64(instanceID:token)>`, en el
     fichero de secretos (SOPS).
2. **Dashboard**: Dashboards → New → Import → sube `infra/grafana/dashboards/api.json` y
   elige los datasources de Prometheus (Mimir) y Tempo del stack en las variables
   *Métricas* y *Trazas*.
3. **Sentry** (región UE, `de.sentry.io`): un proyecto Go para la API (`SENTRY_DSN`, en
   secretos) y uno React para el front (`VITE_SENTRY_DSN` al construir). En los dos,
   desactiva *Data Scrubber: Store IP addresses* (por defecto ya no guarda IPs si
   `send_default_pii` está desactivado, pero comprobarlo no cuesta) y revisa que los
   eventos llegan sin usuario ni cabeceras. Source maps del front: requieren un token de
   Sentry en el CI (queda para 1B).
4. Antes de enviar logs a un proveedor, aplica el ADR 0004 también a ellos (hoy llevan
   `user_id`, como pide el diseño para nuestros propios logs).

## Añadir telemetría

- Un atributo nuevo solo sale si está en la lista de `allowedKeys`
  (`api/platform/kit/telemetry/filter.go`) y en la tabla del ADR 0004, con su porqué.
  Si no, el filtro lo descarta sin avisar.
- Una métrica nueva va en `telemetry.Metrics`; `tenant_id` solo donde el ADR lo permite.
- Una llamada a otro servicio externo: `telemetry.ClientSpan(ctx, tracers, "<servicio>",
  "<operación>")`, nunca la URL ni el cuerpo.
