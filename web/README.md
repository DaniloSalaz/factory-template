# web

Frontend React + TypeScript + Vite: TanStack Router (rutas por fichero, con su plugin de
Vite), TanStack Query, shadcn/ui (Base UI) con Tailwind CSS v4, Vitest + Testing Library y
Playwright. Diseño: `docs/designs/auth-y-tenancy.md` (secciones 5, 6, 10 y 10.1).

```sh
npm run dev        # :5173; /api y /auth van por el proxy de Vite a la API (:8080)
npm test           # Vitest (mise run web:test, entra en mise run ci)
npm run e2e        # Playwright contra la API local (mise run web:e2e; en CI, el job e2e)
npm run routes     # genera src/routeTree.gen.ts con la CLI de TanStack Router (tsr)
npm run build      # routes, luego tsc -b, luego vite build
npm run generate   # orval + la seguridad por ruta, desde api-spec/openapi.gen.yaml
```

## Plataforma y app

|                | `src/platform/`                                                                                                                       | `src/app/`                                                                                                                                                       |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Qué es         | Código de la plantilla: shell y layout, login, tenant, MFA, cuenta, manejo de errores, fetcher, componentes de shadcn (`platform/ui`) | Lo propio de cada app                                                                                                                                            |
| Quién lo edita | La plantilla. Las apps **no** lo tocan                                                                                                | Cada app                                                                                                                                                         |
| Hoy            | Lo del paso 8a y, del 8b, miembros, invitaciones, `/invite` y el renombrado del tenant                                                | Una pantalla de ejemplo (`routes/example.tsx`), su entrada en la navegación (`registry.ts`), sus textos (`texts.ts`), el tema (`theme.css`) y el logo (`brand/`) |

- **Registro**: `src/app/registry.ts` exporta el nombre de la app, su logo y las entradas
  de la navegación del tenant (con el permiso que las muestra, si lo hay). La plataforma lo
  lee solo en `src/platform/app-registry.ts`: es el único fichero de `platform` que importa
  `app`, y `src/platform/architecture.test.ts` lo comprueba (`[platform-imports-app]`).
- **Rutas**: las de la plataforma están listadas en `src/platform/routes.ts` (rutas
  virtuales de TanStack Router, cada una en su fichero). Las de la app son rutas por
  fichero bajo `src/app/routes/` y se montan dentro de `/t/$tenantId`: `example.tsx` es
  `/t/$tenantId/example`, con el layout del tenant (navegación y `GET /tenant` ya
  resueltos). `src/routeTree.gen.ts` lo genera la CLI (`npm run routes`, también al
  principio de `npm run build`) y se commitea; el plugin de Vite lo mantiene al día en
  `npm run dev`. Los dos leen las opciones de `tsr.config.json`. `mise run web:check`
  falla si el commiteado no coincide.
- **Pantallas del tenant de la plataforma**: `/t/$tenantId/members` (`members:read`) e
  `/t/$tenantId/invitations` (`members:invite`) van en la navegación del tenant antes que
  las de la app. Los roles que se pueden asignar u ofrecer salen de `GET /tenant/roles`
  (`can_assign`, `can_offer`), nunca de una lista escrita en el front.
- **Marca**: `src/app/theme.css` (colores, radios y tipografía de shadcn como variables
  CSS; tema neutro por defecto) y `src/app/brand/`. Cambiar de marca es sustituir esos dos
  sitios, sin tocar `platform`. `src/platform/styles.css` solo conecta esas variables con
  Tailwind.
- **Composición**: `src/router.ts` y `src/main.tsx` (fuera de las dos carpetas) unen la
  plataforma, el árbol de rutas y los estilos, como `main.go` en el backend.
- Un componente nuevo de shadcn (`npx shadcn@4.23.0 add <x>`) va a `src/platform/ui`
  (`components.json`).

## Reglas del front

- **Sin `fetch` a mano**: toda llamada usa los hooks o funciones de `src/gen/api.ts`
  (orval, cliente react-query), y los tipos salen de ahí. No hay librería de formularios
  ni de validación.
- `src/gen/` no se edita: lo genera `npm run generate` (`mise run generate`) desde el
  contrato unido, `api-spec/openapi.gen.yaml` (`platform.yaml` + `app.yaml`; ejecuta
  `mise run generate`, no `npm run generate` solo, para que la unión esté al día). `api.ts` es orval; `routeSecurity.ts`, `scripts/gen-route-security.ts`, que lee
  el `security` de cada operación como lo hace `routegen` en la API.
- **El fetcher** (`platform/api/fetcher.ts`, mutator de orval) antepone
  `VITE_API_BASE_URL`, envía la cookie (`credentials: "include"`), pone `X-Tenant-ID` con el
  `$tenantId` de la ruta activa **solo** en las operaciones cuyo contrato exige tenant y
  convierte toda respuesta no 2xx en un `ApiError` con el `code` del Problem Details
  (RFC 9457).
- **Errores en un solo sitio** (`platform/errors/handle.ts`), conectado a la caché de
  TanStack Query (`platform/api/query-client.ts`). Ningún componente decide estos casos:

  | Respuesta                          | Qué hace la plataforma                                           |
  | ---------------------------------- | ---------------------------------------------------------------- |
  | `401`                              | `/login?return_to=<ruta actual>`, nunca con el fragmento         |
  | `403 auth.mfa_required`            | `/account/mfa/verify` (challenge + código) y vuelta a la ruta    |
  | `403 auth.mfa_enrollment_required` | `/account/mfa/enroll` y vuelta a la ruta                         |
  | `403 auth.tenant_forbidden`        | `/tenants` con aviso; si era el último tenant usado, se olvida   |
  | `429`                              | Aviso con la espera de `Retry-After`, si viene                   |
  | Verificación de MFA correcta       | La sesión se sustituye (cookie nueva): se invalida toda la caché |

  El resto de errores los muestra la pantalla con `ErrorText` / `errorMessage`, que da el
  texto por `code` y no muestra nada para los casos de la tabla.
- **Invitaciones** (`/invite#token=…`, diseño 5.6.3): `src/router.ts` llama a
  `captureInviteToken` antes de crear el router. En `/invite` guarda el token en
  `sessionStorage` y quita el fragmento con `history.replaceState`, así que ni el router,
  ni el historial ni ningún `return_to` lo ven. Sin sesión, `GET /me` da `401` y el manejo
  de errores lleva a `/login?return_to=/invite`; al volver, el token sale de
  `sessionStorage` (pase lo que pase) y va a `POST /invitations/accept`. `index.html` pone
  `Referrer-Policy: no-referrer` para toda la app; en producción lo pone también el
  hosting como cabecera (`docs/runbooks/deploy.md`).
- **Errores a Sentry** (`platform/telemetry/sentry.ts`, ADR 0004): solo con
  `VITE_SENTRY_DSN`, iniciado en `src/router.ts` después de capturar el token de
  `/invite`. Sin Session Replay, sin tracing ni `traceparent` hacia la API, y `beforeSend`
  quita usuario, cabeceras, cookies, cuerpo, query string, fragmento y migas. Un error de
  render o de ruta muestra la pantalla genérica (`PlatformErrorBoundary`,
  `RouteErrorScreen`), nunca el error.
- **Listas por cursor** (miembros, invitaciones): `useCursorPages` con la clave generada de
  la operación, para que la caché siga separada por tenant y se invalide con ella.
- **El tenant vive en la URL** (`/t/$tenantId/…`, diseño 10.1): nada de tenant en estado
  global ni en `localStorage`, salvo el último usado, solo para redirigir desde `/`. La
  caché de Query separa los datos de cada tenant (`tenantScopedHash`).
- **Permisos**: ocultar acciones según `GET /tenant` (`can(tenant, permiso)`) es solo UX;
  el servidor decide.
- **Textos** en español, en `platform/texts.ts` (los de la app, en `app/texts.ts`). Sin i18n
  todavía.
- El secret y el URI de un alta de MFA no se guardan en ningún almacenamiento ni se
  registran: solo viven en el estado del componente mientras se muestra el QR.
- Biome (lint y formato) y `tsc -b` estrictos. Las versiones de `package.json` van fijadas
  exactas, sin `^`.

## Tests

- **Unitarios** (`src/**/*.test.ts(x)`, Vitest + Testing Library, jsdom): manejo de errores
  por código (incluidos todos los de miembros e invitaciones), `X-Tenant-ID` según el
  contrato, último tenant, caché por tenant, verificación de MFA, el flujo de `/invite`
  (fragmento quitado antes de crear el router, token nunca en `return_to` ni en la URL,
  `sessionStorage` vacío tras aceptar o fallar) y la regla de arquitectura. Usan `tsconfig.test.json`, que añade los tipos de Node
  sin dárselos al código del navegador.
- **Playwright** (`e2e/`), contra la API real local: Postgres arrancado y migrado
  (`mise run up && mise run migrate`) y la API en marcha (`mise run dev`); Playwright arranca
  Vite si no está. Las sesiones se siembran por SQL como en `docs/runbooks/auth.md`, sin
  AuthKit (`e2e/db.ts`), y se borran al acabar. Los flujos de MFA usan respuestas simuladas
  (`page.route`): no hay fake de WorkOS en el backend. Las capturas quedan en
  `e2e-screenshots/` (no se commitean). El token de una invitación creada desde la pantalla
  solo llega al log de desarrollo, así que el test lo sustituye por uno conocido
  (`knownInviteToken`).
- En CI, el job `e2e` hace lo mismo con Postgres como servicio y la API con una
  `WORKOS_API_KEY` ficticia (el arranque solo comprueba que existe; nada del recorrido
  llama a WorkOS), y sube capturas y trazas como artefactos (`docs/runbooks/ci.md`).
