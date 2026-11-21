# Runbook: despliegue

Todavía no hay despliegue automatizado (llega con el email real de las invitaciones, diseño
5.6.7). Esta página reúne lo que el hosting debe cumplir cuando exista.

## Front (`web/dist`)

Cabeceras que el hosting del front envía en **todas** las respuestas:

| Cabecera | Valor | Por qué |
|---|---|---|
| `Referrer-Policy` | `no-referrer` | El enlace de una invitación lleva el token en el fragmento (`/invite#token=…`, diseño 5.6.3). El front lo quita de la URL al arrancar, pero ninguna página debe enviar un `Referer`. `web/index.html` lo pone también como `<meta name="referrer">`, que no protege las peticiones anteriores a que se lea el HTML |

Todas las rutas del front (`/invite`, `/t/…`) sirven `index.html`: el enrutado es del
navegador (TanStack Router).

## API

- Solo por Cloudflare Tunnel, con `TRUSTED_CLIENT_IP_HEADER` (diseño, sección 3).
- `ALLOWED_ORIGINS` con el origen exacto del front: CORS expone `Retry-After` y
  `X-Request-ID` para que el front, en otro origen, pueda leerlas.
