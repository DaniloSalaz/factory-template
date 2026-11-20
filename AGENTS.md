# AGENTS.md

Plantilla base para SaaS: monolito modular en Go + frontend React. Desarrollador único.

## Stack
- Backend: Go, `net/http` (ServeMux), pgx/v5, sqlc, goose, `log/slog` (JSON).
- Contrato: OpenAPI 3.1 en `api-spec/openapi.yaml` → oapi-codegen (Go, strict server) y orval (TS).
- Frontend: React + TypeScript + Vite, Biome.
- Datos: PostgreSQL 18 (`compose.yaml`). Colas: River (aún no). Secretos: SOPS + age.
- Herramientas y tareas: `mise.toml` (no Makefile ni Taskfile).

## Comandos (`mise run <tarea>`)
| Tarea | Qué hace |
|---|---|
| `up` / `down` | Arranca / para Postgres local |
| `migrate` | Aplica migraciones de `migrations/` con goose |
| `generate` | sqlc + oapi-codegen + orval |
| `dev` | Arranca la API en `:8080` |
| `test` | Tests Go con `-race` (incluye integración con Testcontainers; requiere Docker) |
| `arch` | Tests de arquitectura (límites entre módulos y fixtures de las reglas de lint) |
| `lint` / `fmt` | golangci-lint + actionlint + Biome + tsc / formateo |
| `generate:check` | Regenera y falla si el código generado no coincide con lo commiteado |
| `security` | govulncheck + gitleaks + trivy |
| `ci` | Todo lo que ejecuta el CI, en el mismo orden (ver `docs/runbooks/ci.md`) |

Las variables de entorno de desarrollo vienen de `[env]` en `mise.toml`; `.env` las sobrescribe.

## Estructura
```
api/cmd/api/              composition root (único sitio que cablea todo)
api/internal/platform/    config, db, httpx, logx, pii, apperr (transversal, sin negocio)
api/internal/modules/<m>/
  module.go               API pública del módulo: New(Deps) + Register(mux)
  domain/                 entidades y reglas; NO importa infraestructura (depguard lo impide)
  app/                    casos de uso + puertos (interfaces) que necesitan
  adapters/http/          implementa la interfaz strict generada desde OpenAPI
  adapters/postgres/      implementa los puertos con sqlc
  queries/                SQL para sqlc
api/gen/                  código generado (openapi/<m>, sqlc/<m>)
api-spec/openapi.yaml     contrato HTTP
migrations/               migraciones goose (NNNNN_nombre.sql)
web/src/gen/              cliente TS generado
```
Ejemplo vivo: `api/internal/modules/health`.

## Reglas
- Tipado estricto en Go y TS. Nada de `any` en TS ni `interface{}` sin motivo en Go.
- Errores siempre envueltos con contexto: `fmt.Errorf("hacer x: %w", err)`.
- Sin variables globales ni `init()`; dependencias por constructor (`NewX(deps)`).
- Un módulo no importa el interior de otro módulo (ni su código en `gen/`); solo su `module.go`. Lo comprueba `mise run arch`.
- `platform` no importa `internal/modules`; `platform/apperr` y `platform/authctx` solo importan la stdlib; el dominio solo importa la stdlib y `platform/apperr` (depguard).
- El código generado (`api/gen/`, `web/src/gen/`) no se edita: cambia el contrato o la query y ejecuta `mise run generate`.
- Errores de negocio con `apperr` (`NotFound`, `Validation`, `Wrap`…). Los handlers nunca escriben errores a mano: usan `httpx.WriteProblem` (RFC 9457).
- `os.Getenv` (y cualquier lectura del entorno) solo en `platform/config`; el resto recibe `config.Config` (forbidigo).
- Datos personales o secretos en logs, siempre con los tipos de `pii` (`Email`, `Phone`, `Secret`); el request ID se lee y escribe solo con `logx`.
- Los tipos de `pii` se serializan enmascarados: no van en argumentos de trabajos de cola, columnas `jsonb` ni respuestas de la API. En esas fronteras usa `Reveal()`.
- Un `*apperr.Error` no se modifica después de crearlo.
- No uses `WithGroup` en el logger base (mete dentro del grupo los atributos del contexto); agrupa con `slog.Group` en cada registro.
- Nunca uses un nombre sensible (email, token, password…) como nombre de grupo de slog: slog no enmascara los grupos por su nombre.
- Base de datos: la API conecta como `app_runtime` (RLS siempre); las migraciones, como `app_owner`. Toda tabla con `tenant_id` lleva RLS forzado, `tenant_isolation` y alcance declarado (diseño 7.3); los permisos se conceden explícitamente en la migración que crea la tabla (`docs/runbooks/database.md`).
- Los permisos de `app_runtime` (de tabla, de columna y de ejecución) están fijados en `api/internal/platform/db/schematest/testdata/runtime_privileges.golden`; nunca un `UPDATE` sin columnas. Cambiar ese fichero es una decisión de seguridad: se edita a mano y el PR justifica qué permiso cambia y por qué el diseño lo necesita.
- Una migración fusionada en main es inmutable; los cambios posteriores van en una migración nueva.
- Usuario de sistema reservado: `00000000-0000-7000-8000-000000000001` (deshabilitado, sin login). Úsalo solo para trabajos de sistema.
- Cada cambio va con su test (unitario con fakes de puertos; integración con Testcontainers).
- Comentarios solo para explicar el porqué, no el qué.

## Antes de dar una tarea por terminada
- `mise run ci` en verde (necesita Docker). Es lo mismo que exige el CI para fusionar en `main`.
- Si una regla de lint o de arquitectura se interpone, se corrige el código, no la regla. Sus mensajes dicen qué regla es (`[domain-pure]`, `[module-boundaries]`…) y cómo arreglarlo.
- Una exclusión (`//nolint:<linter>`) solo con un comentario que explique por qué: `//nolint:wrapcheck // motivo`.

## Añadir un endpoint
1. Defínelo en `api-spec/openapi.yaml` con el `tag` del módulo.
2. `mise run generate` e implementa el método en `adapters/http`.
3. Lógica en `app/`, puertos en `app/`, implementación en `adapters/`.
