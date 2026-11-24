# AGENTS.md

Plantilla base para SaaS: monolito modular en Go + frontend React. Desarrollador único. Qué es esta app y sus reglas propias: `APP.md`.

## Stack
- Backend: Go, `net/http` (ServeMux), pgx/v5, sqlc, goose, `log/slog` (JSON).
- Contrato: OpenAPI 3.1 en `api-spec/core.yaml` (plantilla) y `api-spec/app.yaml` (app), unidos por `api/tools/specmerge` en `api-spec/openapi.gen.yaml` → oapi-codegen (Go, strict server, un paquete por módulo con `codegen.yaml`) y orval (TS).
- Frontend: React + TypeScript + Vite, Biome. Datos: PostgreSQL 18 (`compose.yaml`). Colas: River (aún no). Secretos: SOPS + age.
- Observabilidad: OpenTelemetry (OTLP/HTTP; `otelhttp`, `otelpgx`) y Sentry (API y front), en `api/core/kit/telemetry` y `web/src/core/telemetry`; en local, `grafana/otel-lgtm` (`docs/core/runbooks/observability.md`).
- Email: SMTP genérico con `wneessen/go-mail` (Mailpit en desarrollo). Imagen: `ghcr.io/<owner>/factory-template-api`, publicada por `.github/workflows/release.yml`.
- Herramientas y tareas: `mise.toml` (no Makefile ni Taskfile).

## Comandos (`mise run <tarea>`)
| Tarea | Qué hace |
|---|---|
| `hooks:install` | Instala el hook de pre-commit (rechaza commits en `main`); una vez por clon |
| `up` / `down` | Arranca / para Postgres, Mailpit y LGTM locales |
| `obs` / `mail` | Abre Grafana local (LGTM) en el dashboard de la API / Mailpit (`http://localhost:8025`), donde llegan las invitaciones en desarrollo |
| `migrate` | Aplica las migraciones de core (`api/core/migrations/`) y después las de app (`api/app/migrations/`) como `app_owner` con `api migrate` (el mismo subcomando que usa el despliegue) |
| `migration:new <nombre>` | Crea una migración de app en `api/app/migrations/` |
| `migration:new:core <nombre>` | Crea una migración de core en `api/core/migrations/` (solo en factory-template) |
| `generate` | sqlc + unión del contrato (`specmerge`) + oapi-codegen por cada `codegen.yaml` + orval (+ la seguridad por ruta del front) |
| `generate:check` | Regenera y falla si el código generado o `api-spec/openapi.gen.yaml` no coinciden con lo commiteado |
| `dev` | Arranca la API en `:8080` |
| `test` / `test:integration` | Tests unitarios de Go con `-race`, sin Docker / además los de etiqueta `integration` (Testcontainers; requiere Docker) |
| `arch` | Tests de arquitectura de `api/tests/arch` (límites entre módulos y fixtures de las reglas de lint) |
| `lint` / `fmt` | golangci-lint + actionlint + Biome + tsc / formateo |
| `web:test` | Tests unitarios del front (Vitest), incluidas `[core-imports-app]` y `[feature-boundaries]` |
| `web:e2e` | Recorrido en navegador (Playwright) contra la API local (necesita `up`, `migrate` y `dev`); en CI lo ejecuta el job `e2e` |
| `security` | govulncheck + gitleaks + trivy |
| `template:init` / `template:sync` / `template:check` | Convierte un clon de la plantilla en una app / fusiona `template/main` en una rama y actualiza `.template-version` / falla si una ruta de `.template-paths` difiere de la plantilla (pasa en la plantilla); ver `docs/core/runbooks/template-sync.md` |
| `ci` | Todo lo que ejecuta el CI, en el mismo orden (ver `docs/core/runbooks/ci.md`) |

Las variables de entorno de desarrollo vienen de `[env]` en `mise.toml`; `.env` las sobrescribe.

## Zonas (detalle en `docs/core/estructura.md`)
Cada zona tiene tres partes: **`core/`** de la plantilla (una app no la edita; llega por sincronización), **`app/`** de cada app y **`gen/`** generado (nunca a mano).
- Back: `api/core/` (`kit/` transversal, módulos `identity` y `health`, `migrations/`) · `api/app/` (`app.go`, `modules/<m>/`, `migrations/`) · `api/gen/`. `api/*.go` (composition root) y `api/tests/`, `api/tools/` son de core.
- Contrato: `api-spec/core.yaml` [P] · `api-spec/app.yaml` [A] · `api-spec/openapi.gen.yaml` [G].
- Front: `web/src/core/` · `web/src/app/` (`registry.ts`, `features/<f>/`, `routes/`, `shared/`, `texts.ts`, `theme.css`, `brand/`) · `web/src/gen/`. Recorridos: `web/e2e/` [P] y `web/e2e/app/` [A].
- Docs: `docs/core/` (ADR, diseños, runbooks) · `docs/app/` (problema, especificación, diseño de módulos, ADR de la app).
- Enganches únicos: `api/app/app.go` (`app.Roles`, `app.Modules`) y `web/src/app/registry.ts`. Reglas que solo afectan al interior de una carpeta de core, en su `AGENTS.md` (`api/core/identity/`, `api/core/kit/`, `api/core/kit/db/`).

## Reglas
- Tipado estricto en Go y TS. Nada de `any` en TS ni `interface{}` sin motivo en Go. Errores siempre envueltos con contexto: `fmt.Errorf("hacer x: %w", err)`.
- Sin variables globales ni `init()`; dependencias por constructor (`NewX(deps)`).
- Comentarios: solo lo que no se deduce del código que tienen al lado: el porqué (y la alternativa descartada), una invariante garantizada en otro sitio (BD, otro módulo), el contrato de un puerto, una restricción de seguridad o el enlace a su fuente (`auth-y-tenancy §5.2`, ADR; `[doc-refs]`, `mise run arch`). Nunca repetir el nombre o la firma, ni «implementa X» (para eso `var _ X = (*T)(nil)`), ni historia («antes», «ahora», PR, paso): eso va al commit o al ADR. Si cambias el código, actualiza o borra su comentario en el mismo cambio.
- Un módulo no importa el interior de otro módulo (ni su código en `gen/`); solo su `module.go`. El dominio solo importa la stdlib y `core/kit/apperr` (`[domain-pure]`). Lo comprueban `mise run arch` y depguard.
- Nada bajo `api/core/` importa `api/app` ni nada debajo, tampoco de forma indirecta a través de un paquete de fuera de `core/` (un helper de `tests/`, código generado), y solo `package main` (`api/`) importa `app` (`[core-imports-app]`, `mise run arch`; en el front, la regla del mismo nombre). `app/` importa solo `core/kit/*` y el `module.go` de un módulo de core; un módulo de app, además, su propio interior y el `module.go` de otros módulos de app (`[module-boundaries]`).
- El código generado (`api/gen/`, `web/src/gen/`, `api-spec/openapi.gen.yaml`) no se edita: cambia el contrato (`core.yaml` o `app.yaml`) o la query y ejecuta `mise run generate`.
- El tag de una operación es la clave de su módulo: exactamente un tag por operación, el nombre de la carpeta del módulo, cuyo `codegen.yaml` lo incluye solo a él y genera `gen/openapi/<m>/` (paquete `<m>api`). `app.yaml` no redefine nada de `core.yaml` (rutas, `operationId`, componentes, tags, `securitySchemes`, la seguridad raíz ni la cabecera de tenant): referencia sus componentes con `core.yaml#/components/…` (`specmerge`: `[contract-duplicate]`, `[contract-core-owned]`, `[contract-module-key]`).
- Un GET nunca modifica datos: SameSite=Lax envía la cookie en navegaciones GET entre sitios y la protección CSRF por Origin no lo cubriría.
- Los permisos se comprueban en el caso de uso con `Authorizer.Require` (`core/kit/authz`), en cualquier punto, también dentro de su transacción: la denegación se audita en una transacción propia que sobrevive al rollback. Nunca en `adapters/http` (`[authz-in-use-cases]`). Los roles y sus permisos son el catálogo de `app.Roles`; la validación del owner la hace `authz.NewCatalog` y la app no puede saltársela.
- Errores de negocio con `apperr` (`NotFound`, `Validation`, `Wrap`…); un `*apperr.Error` no se modifica después de crearlo. Los handlers nunca escriben errores a mano: usan `httpx.WriteProblem` (RFC 9457).
- `os.Getenv` (y cualquier lectura del entorno) solo en `core/kit/config`; el resto recibe `config.Config` (forbidigo, `[env-only-in-config]`).
- Datos personales o secretos en logs, siempre con los tipos de `pii` (`Email`, `Phone`, `Secret`); el request ID se lee y escribe solo con `logx`. Los tipos de `pii` se serializan enmascarados: no van en argumentos de trabajos de cola, columnas `jsonb` ni respuestas de la API; en esas fronteras usa `Reveal()`.
- No uses `WithGroup` en el logger base (mete dentro del grupo los atributos del contexto); agrupa con `slog.Group` en cada registro. Nunca uses un nombre sensible (email, token, password…) como nombre de grupo: slog no enmascara los grupos por su nombre.
- Datos de un tenant, solo dentro de `db.WithTenantTx`; los flujos sin tenant usan `WithUserTx` o `WithAnonTx`. Las transacciones no se anidan; `db.Independent` solo para escrituras que deben sobrevivir al rollback de quien llama (hoy, la auditoría de `authz.denied`).
- Ningún fichero Go fuera de `core/kit/db` (y de los tests de esquema de `api/tests/db`) llama a `set_config` ni nombra las variables de RLS (`[rls-vars-only-in-db]`, `mise run arch`). Los errores `TN0xx` de las funciones de la BD se traducen con `db.MapError`.
- Base de datos: la API conecta como `app_runtime` (RLS siempre); las migraciones, como `app_owner`. Toda tabla con `tenant_id` lleva RLS forzado, `tenant_isolation` y alcance declarado (auth-y-tenancy §7.3; `docs/core/patterns.md`); los permisos se conceden explícitamente en la migración que crea la tabla (`docs/core/runbooks/database.md`). RLS, `SECURITY DEFINER` y la foto de permisos se comprueban sobre core + app en `api/tests/db` (con `migrationsets.All()`), que puede importar `app/`; `core/` nunca la importa.
- Los permisos de `app_runtime` (de tabla, de columna y de ejecución) están fijados en `api/tests/db/testdata/runtime_privileges.golden`; nunca un `UPDATE` sin columnas. Cambiar ese fichero es una decisión de seguridad: se edita a mano y el PR justifica qué permiso cambia y por qué el diseño lo necesita.
- Una app nunca edita una ruta de `.template-paths`: el cambio se hace en factory-template y llega con `mise run template:sync` (`template:check` en el CI; ADR 0007). Si algo del core estorba, se apunta en `FACTORY_NOTES.md`.
- Migraciones en dos conjuntos con numeración y tabla de versiones propias (`goose_db_version`, `goose_app_db_version`): las de una app en `api/app/migrations/`; las de `api/core/migrations/` solo las cambia la plantilla. Una de app puede usar lo de core; una de core nunca referencia nada de la app (`TestCoreMigrationsApplyAlone`, `[core-migrations-alone]`). Sin migraciones vacías ni de relleno. Una migración fusionada en main es inmutable: los cambios van en una nueva.
- Telemetría (ADR 0004): en trazas, métricas y errores solo salen los atributos de `allowedKeys` (`core/kit/telemetry/filter.go`); nunca uno fuera de ella (ni `user_id`, emails, IPs, argumentos SQL, cuerpos, cabeceras, query strings o tokens). Añadir uno exige ampliar la lista y la tabla del ADR con su porqué. El nombre de un span HTTP es el patrón de `ServeMux`, nunca la ruta real; las llamadas a servicios externos van con `telemetry.ClientSpan`, sin URL ni cuerpo; los 5xx llegan a Sentry por `httpx.WriteProblem`, nunca con `sentry` directamente.
- Email: nunca un texto escrito por usuarios (nombre del tenant, de una persona) en una cabecera (asunto, From); en el cuerpo, sin caracteres de control, recortado y escapado (`html/template`). El token de una invitación solo en el enlace. Los errores de envío no llevan destinatarios.
- Usuario de sistema reservado: `00000000-0000-7000-8000-000000000001` (deshabilitado, sin login). Úsalo solo para trabajos de sistema.
- Cada cambio va con su test (unitario con fakes de puertos; integración con Testcontainers). Un test que necesita Docker lleva `//go:build integration`, esté donde esté: `mise run test` no lo compila y `mise run test:integration` sí. Los de integración, arquitectura y BD van en `api/tests/`; se quedan junto a su código solo si usan algo no exportado, son de `package main` o importan el interior de su módulo (`[module-boundaries]`).
- Ningún fichero que no sea `_test.go` fuera de `api/tests/` importa `chaskicode.dev/saas/tests/...` (`[tests-not-in-prod]`). Desde `core/`, sus tests usan `tests/testkit`, nunca `tests/testkit/migrationsets` ni nada que dependa de la app: `[core-imports-app]` también sigue los imports indirectos.

## Reglas del front (`web/`, detalle en `web/README.md`)
- `web/src/core` no importa `web/src/app`, salvo su registro en `core/app-registry.ts` (`[core-imports-app]`). Una app añade navegación en `app/registry.ts` y su marca en `app/theme.css` y `app/brand/`; no edita `core`.
- Una funcionalidad no importa nada de otra (`[feature-boundaries]`): lo compartido va en `app/shared/`. Los tests del front van en `__tests__/` (los de core, en `core/__tests__/` con las mismas subcarpetas; los de una funcionalidad, en la suya) e importan con el alias `@/`.
- Sin `fetch` a mano: se usa el cliente generado (`web/src/gen/api.ts`) y sus tipos. Sin librerías de formularios ni de validación.
- Los errores de la API se tratan en un solo sitio (`core/errors/handle.ts`): `401`, `403 auth.mfa_required`, `auth.mfa_enrollment_required`, `auth.tenant_forbidden` y `429` nunca los decide un componente. Las pantallas muestran el resto por su `code` con `ErrorText`.
- El tenant actual vive en la URL (`/t/$tenantId`); el fetcher pone `X-Tenant-ID` solo en las operaciones cuyo contrato lo exige. Nada de tenant en estado global ni en `localStorage`, salvo el último usado (solo para redirigir desde `/`). Ocultar acciones según los permisos de `GET /tenant` es solo UX; el servidor decide.
- Textos en español en `core/texts.ts` (los de la app, en `app/texts.ts`). Versiones de npm exactas, sin `^`.

## Añadir un módulo de app
1. Carpeta `api/app/modules/<m>/` con la forma de `api/core/health` (el ejemplo vivo): `module.go` (`New(Deps) (*Module, error)` + `Register(mux)`), `domain/`, `usecases/` (casos de uso + puertos), `adapters/` (`http/`, `postgres/`…), `queries/`, y `codegen.yaml` si tiene endpoints (copia el de `health` cambiando `health` por `<m>`). Sus `Deps` salen de `app.Deps` (`DB`, `Logger`, `Authorizer`, `Telemetry`). Su diseño, en `docs/app/modules/<m>.md`.
2. Regístralo en `app.Modules` (`api/app/app.go`), en el orden en que debe montarse. `main.go` no cambia. Si necesita permisos nuevos, añádelos a los roles de `app.Roles`.
3. Sus endpoints, tablas y pantallas, como en las secciones siguientes, con el `tag` y el nombre del módulo.

## Añadir un endpoint
1. Defínelo en `api-spec/app.yaml` con el `tag` de su módulo (los de core, en `core.yaml`; nunca en `openapi.gen.yaml`). Los componentes de core se referencian sin copiarlos: `$ref: "core.yaml#/components/responses/InternalError"`. Los cuerpos JSON llevan `additionalProperties: false`: así un campo desconocido da `400 unknown_field` (`httpx.KnownBodyFields`). Por defecto exige sesión y `X-Tenant-ID`; sin tenant, `security: [{session: []}]`; público, `security: []`. Los middlewares lo leen de `api/gen/routes` (nunca de una lista a mano).
2. `mise run generate` (genera `api/gen/openapi/<m>/` desde el `codegen.yaml` del módulo, sin tocar `mise.toml`) e implementa el método en `adapters/http`.
3. Lógica en `usecases/`, puertos en `usecases/`, implementación en `adapters/`.

## Añadir una migración
`mise run migration:new <nombre>` crea el fichero en `api/app/migrations/`; las queries, en `queries/` del módulo y `mise run generate`. Aplica las reglas de base de datos de arriba (RLS, permisos explícitos, foto `.golden`) y comprueba con `mise run migrate` y `mise run test:integration`.

## Añadir una pantalla
La funcionalidad va en `web/src/app/features/<f>/` (`pages/`, `components/`, `hooks/`, `__tests__/`), con el mismo nombre que su módulo `api/app/modules/<f>`. Cada pantalla tiene en `web/src/app/routes/` una ruta (montada bajo `/t/$tenantId`) que solo hace `createFileRoute` e importa su página; su entrada de menú, en `app/registry.ts`. El recorrido, en `web/e2e/app/`.

## Proceso
- Antes de empezar, comprueba la rama (`git status -sb`): nunca se trabaja en `main`. Si estás en `main`, o en la rama de un PR ya fusionado, actualiza `main` y crea una rama nueva (`git switch -c <tipo>/<tema>`) antes de tocar nada. El hook de pre-commit (`mise run hooks:install`) rechaza los commits en `main`.
- Nunca te saltes un hook ni una comprobación: ni con `--no-verify` ni creando commits por debajo (`git commit-tree`, `update-ref`…) ni desactivándolos. Si uno impide algo legítimo, para y pregunta.
- Para terminar, `mise run ci` en verde (necesita Docker): es lo mismo que exige el CI para fusionar en `main`.
- Si una regla de lint o de arquitectura se interpone, se corrige el código, no la regla. Sus mensajes dicen qué regla es (`[domain-pure]`, `[module-boundaries]`…) y cómo arreglarlo. Una exclusión (`//nolint:<linter>`) solo con un comentario que explique por qué: `//nolint:wrapcheck // motivo`.
