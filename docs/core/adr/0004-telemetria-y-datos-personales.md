# ADR 0004 — Telemetría sin datos personales: lista de lo permitido, aplicada en código

- Estado: aceptado
- Fecha: 2026-11-21

## Contexto
El paso O1 añade trazas y métricas (OpenTelemetry) y el registro de errores (Sentry),
en la API y en el front. En la fase 1B irán a proveedores gestionados (Grafana Cloud,
Sentry), que son empresas de EE. UU.: lo que se les envía sale de nuestra
infraestructura y, según la configuración, de la UE.

Uno de los clientes previstos es un despacho legal. Sus usuarios, los clientes del
despacho, sus nombres y sus documentos son datos personales y, a menudo, secreto
profesional. El RGPD exige minimización (art. 5.1.c) y una base legal y garantías
(arts. 44–49) para cualquier transferencia internacional. La forma más sencilla de
cumplir es no transferir nada personal: si la telemetría no lleva datos personales, el
proveedor no es encargado de esos datos para ella.

La instrumentación automática no ayuda: la de HTTP añade por defecto la ruta real (con
ids de usuario como `/tenant/members/{userId}`), la query string (`/auth/callback?code=…`)
y la IP del cliente; la de base de datos puede añadir los argumentos de las consultas;
Sentry, por defecto, cabeceras, cookies, cuerpo y usuario. Una convención escrita no
basta: cualquier dependencia nueva o versión nueva puede añadir un atributo.

## Decisión
**Lista de lo permitido, no de lo prohibido.** Todo atributo que no esté en la lista se
descarta en el último punto antes de salir del proceso:

- trazas: un exportador que envuelve al real y deja en cada span solo los atributos
  permitidos (y en sus eventos, solo `exception.type`); la descripción del estado de error
  se sustituye por el código de error;
- métricas: una vista del SDK con filtro de atributos sobre todos los instrumentos;
- Sentry (API y front): un `BeforeSend` que quita usuario, cabeceras, cookies, cuerpo,
  query string, fragmento y migas, deja solo las etiquetas permitidas y enmascara emails y
  tokens en los mensajes.

El filtro vive en `api/core/kit/telemetry` y `web/src/core/telemetry` y tiene
tests que construyen eventos con datos prohibidos y comprueban que salen sin ellos.

### Permitido

| Atributo | Dónde | Por qué es seguro |
|---|---|---|
| `tenant_id` | trazas, errores, la métrica `authz.denied` | UUID opaco de una organización; no identifica a una persona y hace falta para atender a un cliente |
| Ruta: patrón de `ServeMux` (`GET /tenant/members/{userId}`) y `http.route` | trazas, métricas, errores | Plantilla del contrato, sin valores |
| Método, estado HTTP (`http.request.method`, `http.response.status_code`) | trazas, métricas | |
| Código de error (el `code` de Problem Details, `error.type`) | trazas, métricas, errores | Vocabulario cerrado de la API |
| Versión (`service.version`, el SHA) y entorno (`deployment.environment`) | recurso, errores | |
| `service.name`, nombre del servicio externo (`peer.service`: `workos`, `smtp`) y resultado (`result`, vocabulario cerrado) | trazas, métricas | |
| Base de datos: sistema, nombre de la operación o de la consulta de sqlc y **el texto SQL con marcadores (`$1`), sin argumentos** | trazas | El texto es código del repositorio; los valores nunca salen |
| Estadísticas del pool de conexiones | métricas | |
| `trace_id`, `span_id` | trazas, errores, logs | Aleatorios |

### Prohibido (ejemplos; lo no permitido está prohibido)
`user_id`, emails, nombres, IPs (también truncadas), argumentos SQL, cuerpos, cabeceras
`Cookie` y `Authorization` (y cualquier otra), query strings, fragmentos (`#token=…`),
rutas reales con ids y cualquier token. `auth.tenant_forbidden` no lleva ni `tenant_id`:
es la señal de alguien probando tenants ajenos, y el id pedido lo elige quien sondea.

### Alcance y lo que queda fuera
- **Logs**: siguen en stdout, en JSON, con lo que dice el diseño (5.4.4: `user_id` y
  `tenant_id`, emails enmascarados con `pii`); ahora también `trace_id` y `span_id`. No se
  exportan por OTLP. Enviarlos a un proveedor externo exigirá aplicarles este mismo filtro
  (fase 1B).
- **Front**: Sentry sin Session Replay (grabaría la pantalla, con datos de clientes) y sin
  propagación de trazas a la API (`traceparent`) por ahora; sin source maps hasta la fase
  1B (necesitan un secreto en el CI).
- **Exportación**: OTLP por HTTP (`http/protobuf`), configurado con las variables estándar
  `OTEL_EXPORTER_OTLP_*` y `OTEL_TRACES_SAMPLER(_ARG)`. Sin endpoint, la telemetría no hace
  nada.

## Consecuencias
- Añadir un atributo exige añadirlo a la lista (código y esta tabla) con su porqué; si no,
  el filtro lo descarta en silencio. AGENTS.md lo dice.
- Se pierde contexto útil para depurar (qué usuario, qué IP). Para eso están los logs y la
  auditoría, que no salen de nuestra infraestructura.
- Un mensaje de error escrito sin cuidado podría llevar un dato personal; las reglas de
  `pii` lo evitan en origen y el filtro enmascara emails y tokens como segunda barrera.

## Alternativas descartadas
- **Lista de lo prohibido**: cada versión nueva de una instrumentación puede añadir un
  atributo personal que nadie revisó.
- **Seudonimizar `user_id` (hash)**: sigue siendo dato personal con el RGPD (es
  reidentificable con nuestra base de datos) y no aporta nada que los logs no den.
- **Un colector intermedio que filtre**: añade infraestructura y deja el filtro fuera de
  los tests del repositorio. Puede añadirse después, como segunda barrera.
