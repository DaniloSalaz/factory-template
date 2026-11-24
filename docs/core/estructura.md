# Estructura del repo

Estructura de factory-template tal como está construida, tras la reorganización de los PRs 1 a 8 (decisión en [ADR 0005](adr/0005-estructura-plataforma-app-gen.md); el nombre `core` de la zona de la plantilla, en [ADR 0006](adr/0006-zona-core.md)). Lo que aún no existe se marca **(fase 2)** o **(aún no)**, y lo que solo ilustra la forma, **(ejemplo)**. Si el repo y este documento no coinciden, se corrige el que esté mal en el mismo PR.

## Estructura de carpetas

Idea única: **cada zona tiene tres partes, `core/` (de la plantilla), `app/` (de cada app) y `gen/` (generado)**. Marcas: **[P]** core, no se toca desde una app (se cambia en factory-template y llega por sincronización); **[A]** de la app; **[G]** generado, nunca a mano (`mise run generate`); **[C]** compartido, lo tocan ambas partes y se mantiene al mínimo.

```text
factory-template/
│
├── api/                                  BACKEND (Go)
│   ├── go.mod                            [C] module chaskicode.dev/saas (igual en todas las apps, nunca se renombra)
│   ├── go.sum                            [G]
│   ├── main.go                           [P] composition root: config, BD, módulos, servidor
│   ├── migrate.go                        [P] `api migrate`: core primero, app después
│   ├── migrate_integration_test.go       [P] junto a su código: es de `package main`
│   ├── version.go  version_test.go       [P]
│   ├── Dockerfile  .dockerignore         [P]
│   ├── .golangci.yml                     [P] depguard / forbidigo con las reglas de arquitectura
│   ├── sqlc.yaml                         [C] una entrada por módulo
│   │
│   ├── core/                         [P] ── LA PLANTILLA ──
│   │   ├── kit/                              piezas transversales, sin negocio
│   │   │   ├── AGENTS.md  CLAUDE.md          reglas de kit/ y de config/
│   │   │   ├── config/  httpx/  apperr/
│   │   │   ├── authctx/  authz/  logx/
│   │   │   ├── pii/  ratelimit/  clientip/
│   │   │   ├── telemetry/
│   │   │   ├── db/                           transacciones, RLS, MapError, migraciones
│   │   │   │   ├── db.go  tx.go  errors.go
│   │   │   │   ├── migrate.go  queryname.go
│   │   │   │   ├── AGENTS.md  CLAUDE.md      reglas internas de db/
│   │   │   │   ├── *_integration_test.go     junto a su código: usan sus tablas de ejemplo
│   │   │   │   └── testdata/migrations/      tablas de ejemplo (docs/core/patterns.md)
│   │   │   └── page/                         Page[T]: paginación, búsqueda y filtros (aún no)
│   │   ├── identity/
│   │   │   ├── AGENTS.md  CLAUDE.md          reglas internas de identity
│   │   │   ├── module.go  codegen.yaml
│   │   │   ├── *_integration_test.go         junto a su código: importan el interior del módulo
│   │   │   ├── domain/                       sesión, tenant, invitación, MFA
│   │   │   ├── usecases/                     login, tenants, miembros, invitaciones + ports.go
│   │   │   ├── adapters/
│   │   │   │   ├── http/                     entrada: endpoints de auth, tenant, miembros
│   │   │   │   ├── postgres/                 salida: sesiones, tenants, membresías (sqlc)
│   │   │   │   ├── workos/                   salida: proveedor de identidad real
│   │   │   │   ├── fakeidp/                  salida: proveedor falso para tests y e2e
│   │   │   │   ├── smtpsender/               salida: email real (su test de integración, aquí)
│   │   │   │   └── devsender/                salida: email de desarrollo (enlace en el log)
│   │   │   └── queries/identity.sql
│   │   ├── health/                           /healthz y /readyz: el módulo de ejemplo más pequeño
│   │   │   ├── module.go  codegen.yaml
│   │   │   ├── domain/  usecases/  queries/
│   │   │   └── adapters/http/  adapters/postgres/
│   │   └── migrations/                       tabla goose_db_version
│   │       ├── 000*.sql                      00001–00006
│   │       └── migrations.go                 embed + MigrationSet
│   │
│   ├── app/                              [A] ── TU APP ──
│   │   ├── app.go  app_test.go               app.Modules + app.Roles (catálogo de roles)
│   │   ├── modules/                          (ejemplo: aún no hay módulos de app)
│   │   │   └── appointments/                 (ejemplo)
│   │   │       ├── module.go                 ≈ appointment.module.ts: New(deps) + Register(mux)
│   │   │       ├── codegen.yaml              qué parte del contrato genera para este módulo
│   │   │       ├── domain/                   ≈ entities, sin ORM: reglas del negocio
│   │   │       ├── usecases/                 ≈ services: una acción por fichero + ports.go
│   │   │       ├── adapters/                 http/ (entrada), postgres/, whatsapp/… (salida)
│   │   │       └── queries/
│   │   └── migrations/                       tabla goose_app_db_version (puede estar vacía)
│   │       └── migrations.go                 embed + MigrationSet
│   │
│   ├── gen/                              [G] crece con cada módulo
│   │   ├── README.md
│   │   ├── openapi/<m>/                      desde api-spec/openapi.gen.yaml
│   │   ├── sqlc/<m>/                         desde queries/*.sql
│   │   └── routes/                           seguridad por ruta
│   │
│   ├── tests/                            [P] lo que no es unitario
│   │   ├── arch/                             fronteras entre carpetas y módulos, reglas de lint
│   │   │   ├── doc.go  *_test.go
│   │   │   └── testdata/                     fixtures de cada regla
│   │   ├── db/                               RLS, SECURITY DEFINER, funciones, miembros
│   │   │   ├── doc.go  rules.go  functions.go
│   │   │   ├── privileges.go  *_test.go
│   │   │   └── testdata/                     runtime_privileges.golden
│   │   ├── integration/<p>/                  un paquete por su API pública (health, migrations…)
│   │   └── testkit/                          Postgres con Testcontainers
│   │       ├── testkit.go
│   │       └── migrationsets/                core + app (solo para tests/ y app/)
│   │
│   └── tools/                            [P]
│       ├── routegen/                         seguridad por ruta
│       ├── specmerge/                        une core.yaml + app.yaml
│       └── genmodule/                        `mise run module:new name=…` (fase 2)
│
├── web/                                  FRONTEND (React + TS + Vite)
│   ├── package.json  package-lock.json   [C]
│   ├── README.md                         [P] reglas y detalle del front
│   ├── vite.config.ts  vitest.config.ts  [P]
│   ├── tsconfig.json  tsconfig.*.json    [P]
│   ├── biome.json  components.json       [P]
│   ├── playwright.config.ts              [P]
│   ├── orval.config.ts  tsr.config.json  [P]
│   ├── index.html  .gitignore            [P]
│   ├── .env.development  .env.production   [P]
│   ├── scripts/                          [P] gen-route-security.ts
│   ├── e2e/                              [P] recorridos de core
│   │   ├── *.spec.ts  db.ts  fixtures.ts
│   │   ├── global-setup.ts
│   │   └── app/                          [A] recorridos de la app
│   └── src/
│       ├── main.tsx  router.ts           [P]
│       ├── test-setup.ts  vite-env.d.ts  [P]
│       ├── core/                     [P]
│       │   ├── routes.ts  routes/            login, tenants, MFA, miembros, invitaciones
│       │   ├── api/  errors/  tenant/        fetcher, handle.ts, use-cursor-pages.ts
│       │   ├── invite/  telemetry/
│       │   ├── components/  ui/              ui/: shadcn
│       │   ├── texts.ts  paths.ts
│       │   ├── app-types.ts  app-registry.ts   app-registry.ts: único import de app/
│       │   ├── bootstrap.tsx  styles.css
│       │   └── __tests__/                    mismas subcarpetas + architecture.test.ts
│       ├── app/                          [A]
│       │   ├── registry.ts  texts.ts
│       │   ├── theme.css  brand/
│       │   ├── routes/                       URL → página (montadas en /t/$tenantId)
│       │   ├── shared/                       lo compartido entre funcionalidades (aún no)
│       │   └── features/
│       │       └── example/                  mismo nombre que el módulo del back
│       │           ├── pages/
│       │           └── __tests__/
│       └── gen/                          [G] api.ts, routeSecurity.ts, routeTree.gen.ts
│
├── api-spec/
│   ├── core.yaml                     [P]
│   ├── app.yaml                          [A]
│   └── openapi.gen.yaml                  [G] los dos unidos; de aquí leen todos los generadores
│
├── docs/
│   ├── core/                         [P]
│   │   ├── estructura.md  glossary.md
│   │   ├── patterns.md
│   │   └── adr/  designs/  runbooks/
│   └── app/                              [A] punto de entrada de un proyecto nuevo
│       ├── README.md  problem.md
│       ├── spec.md  glossary.md
│       └── modules/  adr/
│
├── infra/                                [P]
│   ├── db/                                   roles y extensiones
│   ├── grafana/                              dashboards de LGTM
│   └── deploy/                               Kamal (aún no)
├── .github/                              [P]
│   ├── workflows/                            ci.yml  release.yml
│   ├── scripts/                              changes.sh, template-{init,sync,check}.sh
│   └── CODEOWNERS  pull_request_template.md
├── .claude/skills/                       [P] (fase 2.3)
├── .vscode/settings.json                 [P] anidado de ficheros (_test.go bajo su fichero)
│
├── AGENTS.md                             [P] reglas para cualquier agente
├── APP.md                                [A] qué es esta app y sus reglas
├── FACTORY_NOTES.md                      [A] fricciones con el core y candidatos a subir a la plantilla
├── CHANGELOG.md                          [P] versiones de la plantilla y cómo migrar a cada una
├── CLAUDE.md                             [P] @AGENTS.md + @APP.md
├── README.md  mise.toml                  [C]
├── compose.yaml  .env.example            [C]
├── secrets.env  .sops.yaml               [A]
├── .template-paths                       [P] rutas de core que vigila el CI de la app (`template:check`)
├── .template-version                     [A] commit de la plantilla del último sync (solo en las apps)
├── renovate.json  .editorconfig
└── .gitignore  LICENSE
```

Dentro de un módulo: `domain/` es lo que es verdad en el negocio; `usecases/` lo que se puede hacer y qué necesita (`ports.go`); `adapters/<tecnología>/` cómo se habla con el exterior. Un adaptador nuevo es una carpeta nueva al lado de las otras, sin tocar `domain/` ni `usecases/`. Los tests unitarios van junto a su fichero (Go lo exige); integración, arquitectura y BD, en `api/tests/`. En el front, los tests van agrupados en `__tests__/` por funcionalidad.

### Tests que se quedan junto a su código
Un test no unitario va en `api/tests/` salvo que use algo no exportado, sea de `package main` o importe el interior de su módulo (fuera, eso rompería `[module-boundaries]`). Hoy son:

| Test | Motivo |
| --- | --- |
| `api/migrate_integration_test.go` | `package main` |
| `api/core/identity/*_integration_test.go` | importan `adapters/fakeidp`, `usecases` y `domain` de identity |
| `api/core/identity/adapters/smtpsender/sender_integration_test.go` | importa el adaptador y `usecases` de identity |
| `api/core/kit/db/*_integration_test.go`, `main_test.go` | usan las tablas de ejemplo de `kit/db/testdata/migrations` |

## Reglas de arquitectura

Todas fallan en `mise run ci` con un mensaje que dice qué regla es y cómo arreglarlo.

| Regla | Qué impide | Dónde se comprueba |
| --- | --- | --- |
| `[domain-pure]` | que `domain/` importe algo distinto de la stdlib y `kit/apperr` | depguard (`api/.golangci.yml`); fixtures en `api/tests/arch/lintrules_test.go` |
| `[core-no-modules]` | que `core/kit` importe un módulo de core | depguard; `lintrules_test.go` |
| `[apperr-stdlib-only]` | que `kit/apperr` importe algo fuera de la stdlib | depguard; `lintrules_test.go` |
| `[authctx-stdlib-only]` | que `kit/authctx` importe algo fuera de la stdlib | depguard; `lintrules_test.go` |
| `[authz-in-use-cases]` | comprobar permisos en `adapters/http` | depguard; `lintrules_test.go` |
| `[env-only-in-config]` | leer el entorno fuera de `kit/config` | forbidigo; `lintrules_test.go` |
| `[module-boundaries]` | que un módulo importe el interior de otro (o su `gen/`) | `api/tests/arch/boundaries_test.go` |
| `[core-imports-app]` | que `core/` importe `app/`, también de forma indirecta | depguard; `api/tests/arch/appimports_test.go`; en el front, `web/src/core/__tests__/architecture.test.ts` |
| `[tests-not-in-prod]` | que código que no es de test importe `tests/…` | `api/tests/arch/testsimports_test.go` |
| `[rls-vars-only-in-db]` | llamar a `set_config` o nombrar las variables de RLS fuera de `kit/db` | `api/tests/arch/rlsvars_test.go` |
| `[feature-boundaries]` | que una funcionalidad del front importe otra | `web/src/core/__tests__/architecture.test.ts` |
| `[contract-duplicate]` | que `app.yaml` repita una ruta, `operationId` o componente de `core.yaml` | `api/tools/specmerge` (`mise run generate`) |
| `[contract-core-owned]` | que `app.yaml` redefina tags, `securitySchemes`, seguridad raíz o cabecera de tenant | `api/tools/specmerge` |
| `[contract-module-key]` | una operación sin exactamente un tag de módulo | `api/tools/specmerge` |
| `[core-migrations-alone]` | que una migración de core dependa de la app | `api/tests/integration/migrations` (`TestCoreMigrationsApplyAlone`) |
| `[runtime-privileges]` | que los permisos de `app_runtime` difieran de la foto `.golden` | `api/tests/db/schema_test.go` |
| `[security-definer]` | una función `SECURITY DEFINER` que no sea de `app_security`, sin `search_path` fijo, ejecutable por otro que `app_runtime` o que reciba el usuario por parámetro | `api/tests/db/functions.go` |

## Decisiones de estructura

- **Module path neutro:** `chaskicode.dev/saas`. Es una etiqueta, no una URL que tenga que resolver: igual en todas las apps y nunca se renombra, para que los ficheros de core sean idénticos en la plantilla y en cada app. La identidad de cada app (nombre, imagen, base de datos) va en configuración.
- **Sin `internal/` ni `cmd/`:** el backend es un único binario que nadie importa; quitarlos ahorra dos niveles.
- **Migraciones separadas** (`core/migrations` y `app/migrations`), cada una con su tabla de versiones de goose (`goose_db_version` y `goose_app_db_version`); `api migrate` aplica primero core. Una migración de app puede referenciar tablas de core, nunca al revés. Así una migración nueva de la plantilla no choca con la numeración de la app.
- **Contrato separado** (`core.yaml` y `app.yaml`), unido por `api/tools/specmerge` en `openapi.gen.yaml` dentro de `mise run generate`; cada módulo genera su paquete desde su `codegen.yaml`.
- **Un único punto de enganche por lado:** `api/app/app.go` (módulos y roles) y `web/src/app/registry.ts` (nombre, logo, menú). El core no importa nada más de `app/`, comprobado por tests de arquitectura.
- **Mismo nombre de funcionalidad en back y front:** `api/app/modules/<x>` ↔ `web/src/app/features/<x>` ↔ `docs/app/modules/<x>.md`.
- **Instrucciones en capas:** `AGENTS.md` de la raíz (lo que necesita cualquier sesión), `AGENTS.md` de carpeta para reglas internas (`core/identity`, `core/kit`, `core/kit/db`), cada uno con su `CLAUDE.md` (`@AGENTS.md`), y `APP.md` para lo propio de la app.
- **Sincronización con la plantilla** ([ADR 0007](adr/0007-sincronizacion-con-apps.md), [runbook](runbooks/template-sync.md)): la app se crea clonando la plantilla (no con el botón "Use this template" de GitHub, que crea una historia sin ancestro común) y recibe mejoras con un remoto `template` y `git merge` (`mise run template:init`, `template:sync`). `.template-paths` lista las rutas [P]; `mise run template:check`, en el CI de la app, falla si alguna difiere del commit de `.template-version`. `api/go.mod` es [C] para que una app pueda añadir dependencias de Go.
- **Generador de módulos (fase 2):** `api/tools/genmodule` (`mise run module:new name=…`) sustituye al antiguo `tools/gen-module/` de la raíz, que se borró en el PR 8.
- **Módulos por empresa (aún no construido):** cada módulo tiene una clave única (su `tag` del contrato) usada igual en back, front y BD, para poder añadir más adelante una tabla `tenant_modules` y un middleware que responda `403 module.disabled`, sin tocar los módulos.
- **De alora** se conservan la organización por funcionalidad, la carpeta de cada funcionalidad del front (`pages/`, `components/`, `hooks/`, `__tests__/`), las convenciones de nombres y la paginación estándar. No se traen las clases base genéricas (`BaseService`), el TypeScript no estricto, los DTOs duplicados a mano ni los ficheros `.backup`.

## Equivalencias con NestJS

| NestJS | Aquí |
| --- | --- |
| Module | `app/modules/<nombre>` + `module.go` |
| Controller | `adapters/http` |
| Service | Caso de uso en `usecases/` |
| Repository (interfaz) | Puerto en `usecases/ports.go` |
| Repository (implementación) | `adapters/postgres` |
| Entity | `domain/` (sin anotaciones de ORM) |
| DTO | Generado del contrato en `gen/openapi/<módulo>` |
| Providers / DI | Constructores explícitos en `main.go` y `app/app.go` (sin contenedor mágico) |
