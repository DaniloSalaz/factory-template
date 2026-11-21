# web

Frontend React + TypeScript + Vite: TanStack Router (rutas por fichero, con su plugin de
Vite), TanStack Query, shadcn/ui (Base UI) con Tailwind CSS v4, Vitest + Testing Library y
Playwright. Diseño: `docs/designs/auth-y-tenancy.md` (secciones 5, 6, 10 y 10.1).

```sh
npm run dev        # :5173; /api y /auth van por el proxy de Vite a la API (:8080)
npm test           # Vitest (mise run web:test, entra en mise run ci)
npm run e2e        # Playwright contra la API local (mise run web:e2e)
npm run build      # vite build (regenera src/routeTree.gen.ts) + tsc -b
npm run generate   # orval + la seguridad por ruta, desde api-spec/openapi.yaml
```

## Plataforma y app

|                | `src/platform/`                                                                                                                       | `src/app/`                                                                                                                                                       |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Qué es         | Código de la plantilla: shell y layout, login, tenant, MFA, cuenta, manejo de errores, fetcher, componentes de shadcn (`platform/ui`) | Lo propio de cada app                                                                                                                                            |
| Quién lo edita | La plantilla. Las apps **no** lo tocan                                                                                                | Cada app                                                                                                                                                         |
| Hoy            | Todo lo del paso 8a                                                                                                                   | Una pantalla de ejemplo (`routes/example.tsx`), su entrada en la navegación (`registry.ts`), sus textos (`texts.ts`), el tema (`theme.css`) y el logo (`brand/`) |

- **Registro**: `src/app/registry.ts` exporta el nombre de la app, su logo y las entradas
  de la navegación del tenant (con el permiso que las muestra, si lo hay). La plataforma lo
  lee solo en `src/platform/app-registry.ts`: es el único fichero de `platform` que importa
  `app`, y `src/platform/architecture.test.ts` lo comprueba (`[platform-imports-app]`).
- **Rutas**: las de la plataforma están listadas en `src/platform/routes.ts` (rutas
  virtuales de TanStack Router, cada una en su fichero). Las de la app son rutas por
  fichero bajo `src/app/routes/` y se montan dentro de `/t/$tenantId`: `example.tsx` es
  `/t/$tenantId/example`, con el layout del tenant (navegación y `GET /tenant` ya
  resueltos). `src/routeTree.gen.ts` lo genera el plugin y se commitea.
- **Marca**: `src/app/theme.css` (colores, radios y tipografía de shadcn como variables
  CSS; tema neutro por defecto) y `src/app/brand/`. Cambiar de marca es sustituir esos dos
  sitios, sin tocar `platform`. `src/platform/styles.css` solo conecta esas variables con
  Tailwind.
- **Composición**: `src/router.ts` y `src/main.tsx` (fuera de las dos carpetas) unen la
  plataforma, el árbol de rutas y los estilos, como `cmd/api` en el backend.
- Un componente nuevo de shadcn (`npx shadcn@4.23.0 add <x>`) va a `src/platform/ui`
  (`components.json`).

## Reglas del front

- **Sin `fetch` a mano**: toda llamada usa los hooks o funciones de `src/gen/api.ts`
  (orval, cliente react-query), y los tipos salen de ahí. No hay librería de formularios
  ni de validación.
- `src/gen/` no se edita: lo genera `npm run generate` (`mise run generate`) desde el
  contrato. `api.ts` es orval; `routeSecurity.ts`, `scripts/gen-route-security.ts`, que lee
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
  | `401`                              | `/login?return_to=<ruta actual>`                                 |
  | `403 auth.mfa_required`            | `/account/mfa/verify` (challenge + código) y vuelta a la ruta    |
  | `403 auth.mfa_enrollment_required` | `/account/mfa/enroll` y vuelta a la ruta                         |
  | `403 auth.tenant_forbidden`        | `/tenants` con aviso; si era el último tenant usado, se olvida   |
  | `429`                              | Aviso con la espera de `Retry-After`, si viene                   |
  | Verificación de MFA correcta       | La sesión se sustituye (cookie nueva): se invalida toda la caché |

  El resto de errores los muestra la pantalla con `ErrorText` / `errorMessage`, que da el
  texto por `code` y no muestra nada para los casos de la tabla.
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
  por código, `X-Tenant-ID` según el contrato, último tenant, caché por tenant, verificación
  de MFA y la regla de arquitectura. Usan `tsconfig.test.json`, que añade los tipos de Node
  sin dárselos al código del navegador.
- **Playwright** (`e2e/`), contra la API real local: Postgres arrancado y migrado
  (`mise run up && mise run migrate`) y la API en marcha (`mise run dev`); Playwright arranca
  Vite si no está. Las sesiones se siembran por SQL como en `docs/runbooks/auth.md`, sin
  AuthKit (`e2e/db.ts`), y se borran al acabar. Los flujos de MFA usan respuestas simuladas
  (`page.route`): no hay fake de WorkOS en el backend. Las capturas quedan en
  `e2e-screenshots/` (no se commitean). Solo local por ahora.
