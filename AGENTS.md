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
| `test` | Tests Go (incluye integración con Testcontainers; requiere Docker) + typecheck web |
| `lint` / `fmt` | golangci-lint + Biome + tsc / formateo |

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
- Un módulo no importa el interior de otro módulo; solo su `module.go`.
- `platform` no importa `internal/modules`. El dominio solo importa la stdlib y `platform/apperr` (depguard lo impide).
- El código generado (`api/gen/`, `web/src/gen/`) no se edita: cambia el contrato o la query y ejecuta `mise run generate`.
- Errores de negocio con `apperr` (`NotFound`, `Validation`, `Wrap`…). Los handlers nunca escriben errores a mano: usan `httpx.WriteProblem` (RFC 9457).
- `os.Getenv` (y cualquier lectura del entorno) solo en `platform/config`; el resto recibe `config.Config`.
- Datos personales o secretos en logs, siempre con los tipos de `pii` (`Email`, `Phone`, `Secret`); el request ID se lee y escribe solo con `logx`.
- Los tipos de `pii` se serializan enmascarados: no van en argumentos de trabajos de cola, columnas `jsonb` ni respuestas de la API. En esas fronteras usa `Reveal()`.
- Un `*apperr.Error` no se modifica después de crearlo.
- No uses `WithGroup` en el logger base (mete dentro del grupo los atributos del contexto); agrupa con `slog.Group` en cada registro.
- Nunca uses un nombre sensible (email, token, password…) como nombre de grupo de slog: slog no enmascara los grupos por su nombre.
- Cada cambio va con su test (unitario con fakes de puertos; integración con Testcontainers).
- Comentarios solo para explicar el porqué, no el qué.
- Antes de dar algo por terminado: `mise run generate && mise run lint && mise run test` sin errores ni diff en generado.

## Añadir un endpoint
1. Defínelo en `api-spec/openapi.yaml` con el `tag` del módulo.
2. `mise run generate` e implementa el método en `adapters/http`.
3. Lógica en `app/`, puertos en `app/`, implementación en `adapters/`.
