# AGENTS.md

Plantilla base para SaaS: monolito modular en Go + frontend React. Desarrollador único.

## Stack
- Backend: Go, `net/http` (ServeMux), pgx/v5, sqlc, goose, `log/slog` (JSON).
- Contrato: OpenAPI 3.1 en `api-spec/platform.yaml` (plantilla) y `api-spec/app.yaml` (app), unidos por `api/tools/specmerge` en `api-spec/openapi.gen.yaml` → oapi-codegen (Go, strict server, un paquete por módulo con `codegen.yaml`) y orval (TS).
- Frontend: React + TypeScript + Vite, Biome.
- Datos: PostgreSQL 18 (`compose.yaml`). Colas: River (aún no). Secretos: SOPS + age.
- Observabilidad: OpenTelemetry (OTLP/HTTP; `otelhttp`, `otelpgx`) y Sentry (API y front), en `api/platform/kit/telemetry` y `web/src/platform/telemetry`; en local, `grafana/otel-lgtm` (`docs/runbooks/observability.md`).
- Email: SMTP genérico con `wneessen/go-mail` (Mailpit en desarrollo). Imagen: `ghcr.io/<owner>/factory-template-api`, publicada por `.github/workflows/release.yml`.
- Herramientas y tareas: `mise.toml` (no Makefile ni Taskfile).

## Comandos (`mise run <tarea>`)
| Tarea | Qué hace |
|---|---|
| `hooks:install` | Instala el hook de pre-commit (rechaza commits en `main`); una vez por clon |
| `up` / `down` | Arranca / para Postgres, Mailpit y LGTM locales |
| `obs` | Abre Grafana local (LGTM) en el dashboard de la API |
| `mail` | Abre Mailpit (`http://localhost:8025`), donde llegan las invitaciones en desarrollo |
| `migrate` | Aplica las migraciones de plataforma (`api/platform/migrations/`) y después las de app (`api/app/migrations/`) como `app_owner` con `api migrate` (el mismo subcomando que usa el despliegue) |
| `migration:new <nombre>` | Crea una migración de app en `api/app/migrations/` |
| `migration:new:platform <nombre>` | Crea una migración de plataforma en `api/platform/migrations/` (solo en factory-template) |
| `generate` | sqlc + unión del contrato (`specmerge`) + oapi-codegen por cada `codegen.yaml` + orval (+ la seguridad por ruta del front) |
| `dev` | Arranca la API en `:8080` |
| `test` | Tests unitarios de Go con `-race`; no necesita Docker |
| `test:integration` | Tests con la etiqueta `integration` (Testcontainers; requiere Docker), unitarios incluidos |
| `arch` | Tests de arquitectura de `api/tests/arch` (límites entre módulos y fixtures de las reglas de lint) |
| `lint` / `fmt` | golangci-lint + actionlint + Biome + tsc / formateo |
| `generate:check` | Regenera y falla si el código generado o `api-spec/openapi.gen.yaml` no coinciden con lo commiteado |
| `web:test` | Tests unitarios del front (Vitest), incluida la regla `[platform-imports-app]` |
| `web:e2e` | Recorrido en navegador (Playwright) contra la API local (necesita `up`, `migrate` y `dev`); en CI lo ejecuta el job `e2e` |
| `security` | govulncheck + gitleaks + trivy |
| `ci` | Todo lo que ejecuta el CI, en el mismo orden (ver `docs/runbooks/ci.md`) |

Las variables de entorno de desarrollo vienen de `[env]` en `mise.toml`; `.env` las sobrescribe.

## Estructura
```
api/*.go                  composition root: main.go, migrate.go, version.go (único sitio que cablea todo; una app nunca lo edita)
api/platform/kit/         config, db, httpx, logx, pii, apperr, telemetry… (transversal, sin negocio)
api/platform/migrations/  migraciones goose de plataforma (tabla `goose_db_version`); solo las cambia la plantilla
api/platform/<m>/         módulos de plataforma (identity, health)
  module.go               API pública del módulo: New(Deps) + Register(mux)
  codegen.yaml            qué genera oapi-codegen para el módulo: include-tags [<m>], package <m>api (solo si tiene endpoints)
  domain/                 entidades y reglas; NO importa infraestructura (depguard lo impide)
  usecases/               casos de uso + puertos (interfaces) que necesitan
  adapters/http/          implementa la interfaz strict generada desde OpenAPI
  adapters/postgres/      implementa los puertos con sqlc
  queries/                SQL para sqlc
api/app/app.go            único enganche de la app: app.Roles (catálogo de roles) y app.Modules (sus módulos)
api/app/modules/<m>/      módulos de la app, con la misma forma que platform/<m>
api/app/migrations/       migraciones goose de la app (tabla `goose_app_db_version`), nuevas con `mise run migration:new <nombre>`
api/gen/                  código generado (openapi/<m>, sqlc/<m>, routes)
api/tests/                lo que no es unitario (los unitarios van junto a su fichero)
  arch/  (+ testdata/)    reglas de arquitectura y sus fixtures (etiqueta `arch`)
  db/    (+ testdata/)    RLS, funciones de la BD, miembros, invitaciones y foto de permisos, sobre plataforma + app
  integration/<p>/        integración de un paquete o módulo por su API pública (`package <p>_test`)
  testkit/                Postgres con Testcontainers; `migrationsets.All()` = plataforma + app
api/tools/                routegen (seguridad por ruta) y specmerge (unión del contrato)
api-spec/platform.yaml    contrato HTTP de la plantilla (una app no lo edita)
api-spec/app.yaml         contrato HTTP de la app
api-spec/openapi.gen.yaml los dos unidos (generado); de aquí leen todos los generadores
web/src/gen/              cliente TS generado (orval) y seguridad por ruta (scripts/gen-route-security.ts)
web/src/platform/         front de la plantilla: shell, auth, tenant, MFA, miembros, invitaciones, errores, fetcher, ui (shadcn)
web/src/app/              lo propio de cada app: rutas bajo /t/$tenantId, registry.ts, theme.css, brand/
```
Ejemplo vivo: `api/platform/health`.

## Reglas
- Tipado estricto en Go y TS. Nada de `any` en TS ni `interface{}` sin motivo en Go.
- Errores siempre envueltos con contexto: `fmt.Errorf("hacer x: %w", err)`.
- Sin variables globales ni `init()`; dependencias por constructor (`NewX(deps)`).
- Un módulo no importa el interior de otro módulo (ni su código en `gen/`); solo su `module.go`. Lo comprueba `mise run arch`.
- `platform/kit` no importa ningún módulo de plataforma (`platform/<m>`, depguard `[platform-no-modules]`); `platform/kit/apperr` y `platform/kit/authctx` solo importan la stdlib; el dominio solo importa la stdlib y `platform/kit/apperr` (depguard).
- Nada bajo `api/platform/` importa `api/app` ni nada debajo, tampoco de forma indirecta a través de un paquete de fuera de `platform/` (un helper de `tests/`, código generado), y solo `package main` (`api/`) importa `app` (`[platform-imports-app]`, `mise run arch`; en el front, la regla del mismo nombre). `app/` importa solo `platform/kit/*` y el `module.go` de un módulo de plataforma; un módulo de app, además, su propio interior y el `module.go` de otros módulos de app (`[module-boundaries]`).
- El código generado (`api/gen/`, `web/src/gen/`, `api-spec/openapi.gen.yaml`) no se edita: cambia el contrato (`platform.yaml` o `app.yaml`) o la query y ejecuta `mise run generate`.
- El tag de una operación es la clave de su módulo: cada operación tiene exactamente un tag, el nombre de la carpeta del módulo, cuyo `codegen.yaml` lo incluye solo a él y genera `gen/openapi/<m>/` (paquete `<m>api`). `app.yaml` no redefine nada de `platform.yaml` (rutas, `operationId`, componentes, tags, `securitySchemes`, la seguridad raíz ni la cabecera de tenant): referencia sus componentes con `platform.yaml#/components/…`. Lo comprueba `specmerge` en `mise run generate` (`[contract-duplicate]`, `[contract-platform-owned]`, `[contract-module-key]`).
- Un GET nunca modifica datos: SameSite=Lax envía la cookie en navegaciones GET entre sitios y la protección CSRF por Origin no lo cubriría.
- Los permisos se comprueban en el caso de uso con `Authorizer.Require` (`platform/kit/authz`), en cualquier punto, también dentro de su transacción: la denegación se audita en una transacción propia que sobrevive al rollback. Nunca en `adapters/http` (depguard, `[authz-in-use-cases]`). Los roles y sus permisos son el catálogo de `app.Roles` (`api/app/app.go`); la validación del owner la hace `authz.NewCatalog` y la app no puede saltársela.
- Errores de negocio con `apperr` (`NotFound`, `Validation`, `Wrap`…). Los handlers nunca escriben errores a mano: usan `httpx.WriteProblem` (RFC 9457).
- `os.Getenv` (y cualquier lectura del entorno) solo en `platform/kit/config`; el resto recibe `config.Config` (forbidigo).
- Datos personales o secretos en logs, siempre con los tipos de `pii` (`Email`, `Phone`, `Secret`); el request ID se lee y escribe solo con `logx`.
- Los tipos de `pii` se serializan enmascarados: no van en argumentos de trabajos de cola, columnas `jsonb` ni respuestas de la API. En esas fronteras usa `Reveal()`.
- Un `*apperr.Error` no se modifica después de crearlo.
- No uses `WithGroup` en el logger base (mete dentro del grupo los atributos del contexto); agrupa con `slog.Group` en cada registro.
- Nunca uses un nombre sensible (email, token, password…) como nombre de grupo de slog: slog no enmascara los grupos por su nombre.
- Las transacciones no se anidan; `db.Independent` solo para escrituras que deben sobrevivir al rollback de quien llama (hoy, la auditoría de `authz.denied`).
- Las membresías no se escriben directamente: `app_runtime` solo tiene `SELECT` sobre `memberships`. Se crean con `create_tenant` y `accept_invitation`, y su rol cambia o se borran solo con `change_member_role`, `remove_member` y `leave_tenant`, que bloquean el tenant y nunca lo dejan sin owner (ADR 0003). La jerarquía (quién gestiona a quién) se decide en `domain` con la inclusión de permisos del catálogo, nunca comparando nombres de rol; la BD solo comprueba que los roles en los que se basó Go no cambiaron.
- Orden de bloqueo en la BD: fila del tenant → invitaciones → membresías. Quien escribe invitaciones bloquea antes el tenant (`FOR KEY SHARE`) y relee el rol del actor; las funciones de miembros lo bloquean `FOR UPDATE`. Otro orden puede interbloquearse con una degradación (`40P01`).
- El token de una invitación (y su hash) no va en ningún log, respuesta ni auditoría; solo el adaptador de desarrollo de `InvitationSender` escribe el enlace, y no arranca fuera de desarrollo. El envío se hace después del commit y su fallo solo se registra (el remedio es reenviar).
- Datos de un tenant, solo dentro de `db.WithTenantTx`; los flujos sin tenant usan `WithUserTx` o `WithAnonTx`. Ningún fichero Go fuera de `platform/kit/db` (y de los tests de esquema de `api/tests/db`) llama a `set_config` ni nombra las variables de RLS (`mise run arch`). Los errores `TN0xx` de las funciones de la BD se traducen con `db.MapError`.
- Base de datos: la API conecta como `app_runtime` (RLS siempre); las migraciones, como `app_owner`. Toda tabla con `tenant_id` lleva RLS forzado, `tenant_isolation` y alcance declarado (diseño 7.3); los permisos se conceden explícitamente en la migración que crea la tabla (`docs/runbooks/database.md`).
- Los permisos de `app_runtime` (de tabla, de columna y de ejecución) están fijados en `api/tests/db/testdata/runtime_privileges.golden`; nunca un `UPDATE` sin columnas. Cambiar ese fichero es una decisión de seguridad: se edita a mano y el PR justifica qué permiso cambia y por qué el diseño lo necesita.
- Migraciones en dos conjuntos con numeración y tabla de versiones propias (`docs/runbooks/database.md`): las de una app van en `api/app/migrations/` (`mise run migration:new`); las de `api/platform/migrations/` solo las cambia la plantilla. Una de app puede usar lo de plataforma; una de plataforma nunca referencia nada de la app (`TestPlatformMigrationsApplyAlone`, `[platform-migrations-alone]`). Sin migraciones vacías ni de relleno.
- Una migración fusionada en main es inmutable; los cambios posteriores van en una migración nueva.
- Las reglas de RLS, de `SECURITY DEFINER` y la foto de permisos se comprueban sobre plataforma + app en `api/tests/db` (con `migrationsets.All()`), que puede importar `app/`; `platform/` nunca la importa.
- MFA (ADR 0002): opcional, exigida solo por los roles del catálogo que lo dicen. Una sesión solo tiene MFA por `sessions.mfa_auth_time`, que fija únicamente `mark_session_mfa` tras un `valid: true` del proveedor (y rota el token). Nunca se deduce del login ni de que el usuario tenga un factor.
- Fuera de development (`staging`, `production`): todas las URL y orígenes del navegador y del proveedor de identidad son `https`, y el SMTP es obligatorio; config lo valida al arrancar. Una opción solo para desarrollo (como `SMTP_TLS_SKIP_VERIFY`) se rechaza fuera de él.
- Email: nunca un texto escrito por usuarios (nombre del tenant, de una persona) en una cabecera (asunto, From); en el cuerpo, sin caracteres de control, recortado y escapado (`html/template`). El token de una invitación solo en el enlace. Los errores de envío no llevan destinatarios.
- Las migraciones (`api/platform/migrations` y `api/app/migrations`) van dentro del binario; el despliegue ejecuta `api migrate` (plataforma primero, app después) antes de cambiar de versión. La versión del binario (`-X main.buildVersion`) sale en los logs y en `/healthz`.
- Telemetría (ADR 0004): en trazas, métricas y errores solo salen los atributos de su lista (`allowedKeys` en `platform/kit/telemetry/filter.go`); nunca añadas uno fuera de ella (ni `user_id`, emails, IPs, argumentos SQL, cuerpos, cabeceras, query strings o tokens). Añadir uno exige ampliar la lista y la tabla del ADR con su porqué. El nombre de un span HTTP es el patrón de `ServeMux`, nunca la ruta real; las llamadas a servicios externos van con `telemetry.ClientSpan`, sin URL ni cuerpo; los errores 5xx llegan a Sentry por `httpx.WriteProblem`, nunca con `sentry` directamente.
- Usuario de sistema reservado: `00000000-0000-7000-8000-000000000001` (deshabilitado, sin login). Úsalo solo para trabajos de sistema.
- Cada cambio va con su test (unitario con fakes de puertos; integración con Testcontainers).
- Un test que necesita Docker lleva `//go:build integration`, esté donde esté: `mise run test` no lo compila y `mise run test:integration` sí. Los de integración, arquitectura y BD van en `api/tests/`; se quedan junto a su código solo si usan algo no exportado, son de `package main` o importan el interior de su módulo (`[module-boundaries]`).
- Ningún fichero que no sea `_test.go` fuera de `api/tests/` importa `chaskicode.dev/saas/tests/...` (`[tests-not-in-prod]`, `mise run arch`). Desde `platform/`, sus tests usan `tests/testkit`, nunca `tests/testkit/migrationsets` ni nada que dependa de la app: `[platform-imports-app]` también sigue los imports indirectos.
- Comentarios solo para explicar el porqué, no el qué.

## Reglas del front (`web/`, detalle en `web/README.md`)
- `web/src/platform` es código de la plantilla y no importa `web/src/app`, salvo su registro en `platform/app-registry.ts` (`[platform-imports-app]`, `mise run web:test`). Una app añade pantallas en `app/routes/`, entradas de navegación en `app/registry.ts` y su marca en `app/theme.css` y `app/brand/`; no edita `platform`.
- Sin `fetch` a mano: se usa el cliente generado (`web/src/gen/api.ts`) y sus tipos. Sin librerías de formularios ni de validación.
- Los errores de la API se tratan en un solo sitio (`platform/errors/handle.ts`): `401`, `403 auth.mfa_required`, `auth.mfa_enrollment_required`, `auth.tenant_forbidden` y `429` nunca los decide un componente. Las pantallas muestran el resto por su `code` con `ErrorText`.
- El tenant actual vive en la URL (`/t/$tenantId`); el fetcher pone `X-Tenant-ID` solo en las operaciones cuyo contrato lo exige. Nada de tenant en estado global ni en `localStorage`, salvo el último usado (solo para redirigir desde `/`).
- Ocultar acciones según los permisos de `GET /tenant` es solo UX; el servidor decide.
- Textos en español en `platform/texts.ts` (los de la app, en `app/texts.ts`). Versiones de npm exactas, sin `^`.
- El secret y el URI de MFA no se guardan en ningún almacenamiento ni se registran.

## Antes de empezar una tarea
- Comprueba la rama (`git status -sb`): nunca se trabaja en `main`. Si estás en `main`, o en la rama de un PR ya fusionado, actualiza `main` y crea una rama nueva (`git switch -c <tipo>/<tema>`) antes de tocar nada. El hook de pre-commit (`mise run hooks:install`, una vez por clon) rechaza los commits en `main`.
- Nunca te saltes un hook ni una comprobación: ni con `--no-verify` ni creando commits por debajo (`git commit-tree`, `update-ref`…) ni desactivándolos. Si uno impide algo legítimo, para y pregunta.

## Antes de dar una tarea por terminada
- `mise run ci` en verde (necesita Docker). Es lo mismo que exige el CI para fusionar en `main`.
- Si una regla de lint o de arquitectura se interpone, se corrige el código, no la regla. Sus mensajes dicen qué regla es (`[domain-pure]`, `[module-boundaries]`…) y cómo arreglarlo.
- Una exclusión (`//nolint:<linter>`) solo con un comentario que explique por qué: `//nolint:wrapcheck // motivo`.

## Añadir un endpoint
1. Defínelo en `api-spec/app.yaml` con el `tag` de su módulo (los de plataforma, en `api-spec/platform.yaml`; nunca en `openapi.gen.yaml`, que se regenera). Los componentes de plataforma se referencian sin copiarlos: `$ref: "platform.yaml#/components/responses/InternalError"`. Los cuerpos JSON llevan `additionalProperties: false`: así un campo desconocido da `400 unknown_field` (`httpx.KnownBodyFields`) en vez de ignorarse. Por defecto exige sesión y `X-Tenant-ID`; si no necesita tenant, `security: [{session: []}]`, y si es público, `security: []`. Los middlewares lo leen de `api/gen/routes` (nunca de una lista de rutas a mano).
2. `mise run generate` (genera `api/gen/openapi/<m>/` desde el `codegen.yaml` del módulo, sin tocar `mise.toml`) e implementa el método en `adapters/http`.
3. Lógica en `usecases/`, puertos en `usecases/`, implementación en `adapters/`.

## Añadir un módulo de app
1. Carpeta `api/app/modules/<m>/` con la forma de `api/platform/health`: `module.go` (`New(Deps) (*Module, error)` + `Register(mux)`), `domain/`, `usecases/`, `adapters/`, `queries/`, y `codegen.yaml` si tiene endpoints (copia el de `api/platform/health` cambiando `health` por `<m>`). Sus `Deps` salen de `app.Deps` (`DB`, `Logger`, `Authorizer`, `Telemetry`).
2. Regístralo en `app.Modules` (`api/app/app.go`), en el orden en que debe montarse. `main.go` no cambia.
3. Si necesita permisos nuevos, añádelos a los roles de `app.Roles`.
4. Sus endpoints, como en "Añadir un endpoint", con el `tag` del módulo.
