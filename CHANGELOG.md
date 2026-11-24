# Changelog

Cambios de factory-template que llegan a las apps con `mise run template:sync`. Formato
[Keep a Changelog](https://keepachangelog.com/es-ES/1.1.0/); versiones con
[SemVer](https://semver.org/lang/es/): la mayor cambia cuando una app tiene que adaptar
`app/` o un fichero [C] para seguir compilando. Cada versión lleva **Cómo migrar**: qué
hacer en una app que sincroniza desde la versión anterior. Antes de sincronizar, lee las
versiones nuevas ([runbook](docs/core/runbooks/template-sync.md#antes-de-sincronizar-el-changelog)).

## Sin publicar

## 0.1.0 - 2026-11-24

Primera versión de la que nacen apps.

### Añadido
- Monolito modular en Go (`net/http`, pgx, sqlc, goose, `log/slog`) y front en React +
  TypeScript + Vite, con el contrato OpenAPI 3.1 en `api-spec/core.yaml` + `api-spec/app.yaml`
  unidos por `specmerge` ([ADR 0001](docs/core/adr/0001-stack-y-arquitectura.md)).
- Módulos de core `identity` (login con WorkOS, tenants, miembros, invitaciones y MFA por
  rol, [ADR 0002](docs/core/adr/0002-mfa-opcional-por-rol.md) y
  [ADR 0003](docs/core/adr/0003-invariante-owner.md)) y `health`.
- PostgreSQL 18 con RLS forzado por tenant, roles `app_owner` / `app_runtime` y la foto de
  permisos `runtime_privileges.golden`.
- OpenTelemetry y Sentry sin datos personales ([ADR 0004](docs/core/adr/0004-telemetria-y-datos-personales.md)),
  email por SMTP e imagen publicada en GHCR.
- Estructura en zonas `core/` · `app/` · `gen/` con sus reglas de arquitectura
  ([ADR 0005](docs/core/adr/0005-estructura-plataforma-app-gen.md), [estructura](docs/core/estructura.md)).
- Sincronización con las apps ([ADR 0007](docs/core/adr/0007-sincronizacion-con-apps.md)):
  `.template-paths`, `.template-version`, las tareas `template:init`, `template:sync` y
  `template:check` (en `mise run ci` y en el job `template` del CI), `FACTORY_NOTES.md` y el
  [runbook](docs/core/runbooks/template-sync.md).

### Cambiado
- La zona de la plantilla se llama `core` en vez de `platform`
  ([ADR 0006](docs/core/adr/0006-zona-core.md)).
- `api/go.mod` pasa de [P] a [C]: una app puede añadir dependencias de Go.

### Cómo migrar
- **App nueva:** clona la plantilla y ejecuta `mise run template:init`
  ([runbook](docs/core/runbooks/template-sync.md#crear-una-app-nueva)).
- **App nacida antes de 0.1.0** (sin `.template-version`): añade el remoto
  (`git remote add template <url de factory-template>`), escribe en `.template-version` el
  commit de la plantilla del que salió o con el que sincronizó por última vez
  (`git merge-base HEAD template/main`) y después ejecuta `mise run template:sync`.
- **Si la app tenía imports de `platform/`:** en el merge de este sync, cambia
  `chaskicode.dev/saas/platform/...` por `chaskicode.dev/saas/core/...` y `@/platform/...`
  por `@/core/...` en `app/`; las rutas de carpeta las sigue git.
