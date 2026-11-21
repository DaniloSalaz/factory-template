# Runbook: despliegue

Todavía no hay despliegue automatizado (servidor, Cloudflare y copias llegan en S2 a S4).
La imagen ya se publica (`docs/runbooks/release.md`). Esta página reúne lo que el hosting
debe cumplir.

## Front (`web/dist`)

Cabeceras que el hosting del front envía en **todas** las respuestas:

| Cabecera | Valor | Por qué |
|---|---|---|
| `Referrer-Policy` | `no-referrer` | El enlace de una invitación lleva el token en el fragmento (`/invite#token=…`, diseño 5.6.3). El front lo quita de la URL al arrancar, pero ninguna página debe enviar un `Referer`. `web/index.html` lo pone también como `<meta name="referrer">`, que no protege las peticiones anteriores a que se lea el HTML |

Todas las rutas del front (`/invite`, `/t/…`) sirven `index.html`: el enrutado es del
navegador (TanStack Router).

## API

- La imagen de `ghcr.io/<owner>/factory-template-api:<sha>`; antes de cambiar de versión,
  `api migrate` (`docs/runbooks/release.md`).
- `APP_ENV=staging` o `production`. Con cualquiera de los dos, la API no arranca si
  `FRONTEND_ORIGIN`, `FRONT_BASE_URL`, `WORKOS_REDIRECT_URI` o algún origen de
  `ALLOWED_ORIGINS` no es `https`, ni sin SMTP (`docs/runbooks/email.md`).
- Solo por Cloudflare Tunnel, con `TRUSTED_CLIENT_IP_HEADER` (diseño, sección 3).
- Telemetría opcional: `OTEL_EXPORTER_OTLP_*` (con las cabeceras de credenciales en
  secretos) y `SENTRY_DSN`; sin ellas, la API no envía nada (`docs/runbooks/observability.md`).
- `ALLOWED_ORIGINS` con el origen exacto del front: CORS expone `Retry-After` y
  `X-Request-ID` para que el front, en otro origen, pueda leerlas.
