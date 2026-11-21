# Estructura del repo

Estructura objetivo de factory-template. Copia de la sección 5 de «Fábrica de software: stack y arquitectura». Si el código contradice este documento, gana el documento.

## Estructura de carpetas

Decidida el 21 de noviembre de 2026. Idea única: **cada zona tiene tres partes, `platform/` (de la plantilla), `app/` (de cada app) y `gen/` (generado)**. Marcas: **[P]** plataforma, no se toca desde una app (se cambia en factory-template y llega por sincronización); **[A]** de la app; **[G]** generado, nunca a mano (`mise run generate`); **[C]** compartido, lo tocan ambas partes y se mantiene al mínimo.

```text
factory-template/
│
├── api/                                  BACKEND (Go)
│   ├── go.mod                       [P]  module chaskicode.dev/saas (igual en todas las apps, nunca se renombra)
│   ├── go.sum                       [G]
│   ├── main.go                      [P]  arranque: config, BD, módulos, servidor
│   ├── migrate.go                   [P]  `api migrate`: plataforma primero, app después
│   ├── version.go                   [P]
│   ├── Dockerfile  .golangci.yml    [P]
│   ├── sqlc.yaml                    [C]  una entrada por módulo
│   │
│   ├── platform/                    [P]  ── LA PLANTILLA ──
│   │   ├── kit/                          piezas transversales, sin negocio
│   │   │   ├── config/  db/  httpx/  apperr/  authctx/  authz/
│   │   │   ├── logx/  pii/  ratelimit/  clientip/  telemetry/
│   │   │   └── page/                     Page[T]: paginación, búsqueda y filtros
│   │   ├── identity/
│   │   │   ├── AGENTS.md                 reglas internas de identity
│   │   │   ├── module.go
│   │   │   ├── domain/                   sesión, tenant, invitación, MFA
│   │   │   ├── usecases/                 login, tenants, miembros, invitaciones + ports.go
│   │   │   ├── adapters/
│   │   │   │   ├── http/                 entrada: endpoints de auth, tenant, miembros
│   │   │   │   ├── postgres/             salida: sesiones, tenants, membresías (sqlc)
│   │   │   │   ├── workos/               salida: proveedor de identidad real
│   │   │   │   ├── fakeidp/              salida: proveedor falso para tests y e2e
│   │   │   │   ├── smtpsender/           salida: email real
│   │   │   │   └── devsender/            salida: email de desarrollo (escribe el enlace en el log)
│   │   │   └── queries/identity.sql
│   │   ├── health/                       /healthz y /readyz: el módulo de ejemplo más pequeño
│   │   │   ├── module.go  domain/  usecases/  queries/
│   │   │   └── adapters/  http/  postgres/
│   │   └── migrations/                   00001–00006, con su propia tabla de versiones
│   │
│   ├── app/                         [A]  ── TU APP ──
│   │   ├── app.go                        módulos de la app + catálogo de roles
│   │   ├── modules/
│   │   │   └── appointments/
│   │   │       ├── module.go             ≈ appointment.module.ts: New(deps) + Register(mux)
│   │   │       ├── codegen.yaml          qué parte del contrato genera para este módulo
│   │   │       ├── domain/               ≈ entities, sin ORM: reglas del negocio
│   │   │       │   ├── appointment.go
│   │   │       │   └── appointment_test.go
│   │   │       ├── usecases/             ≈ services: una acción por fichero
│   │   │       │   ├── ports.go          lo que necesita: AppointmentStore, Notifier…
│   │   │       │   ├── create_appointment.go
│   │   │       │   └── create_appointment_test.go   (con fakes de los puertos)
│   │   │       ├── adapters/
│   │   │       │   ├── http/             entrada ≈ controllers: HTTP → caso de uso
│   │   │       │   ├── postgres/         salida: implementa AppointmentStore con sqlc
│   │   │       │   └── whatsapp/         salida: implementa Notifier (cuando haga falta)
│   │   │       └── queries/
│   │   │           └── appointments.sql
│   │   └── migrations/                   tablas de la app, con su propia tabla de versiones
│   │
│   ├── gen/                         [G]  crece con cada módulo
│   │   ├── openapi/<módulo>/             desde api-spec
│   │   ├── sqlc/<módulo>/                desde queries/*.sql
│   │   └── routes/                       seguridad por ruta
│   │
│   ├── tests/                       [P]  lo que no es unitario
│   │   ├── integration/                  Postgres real (Testcontainers)
│   │   ├── arch/  (+ testdata/)          fronteras entre carpetas y módulos
│   │   ├── db/                           RLS y foto de permisos (.golden)
│   │   └── testkit/                      helpers compartidos
│   │
│   └── tools/                       [P]
│       ├── routegen/
│       └── genmodule/                    `mise run module:new name=…` (fase 2)
│
├── web/                                  FRONTEND (React + TS + Vite)
│   ├── package.json                 [C]
│   ├── vite.config.ts  tsconfig*.json  biome.json  vitest.config.ts
│   │   playwright.config.ts  orval.config.ts  tsr.config.json  components.json  index.html   [P]
│   ├── scripts/                     [P]  gen-route-security.ts
│   ├── e2e/                         [P]  recorridos de plataforma
│   │   └── app/                     [A]  recorridos de la app
│   └── src/
│       ├── main.tsx  router.ts      [P]
│       ├── platform/                [P]
│       │   ├── routes.ts  routes/        login, tenants, MFA, miembros, invitaciones
│       │   ├── api/  errors/  tenant/  invite/
│       │   ├── components/  ui/  hooks/  (useListPage)
│       │   ├── texts.ts  app-types.ts  app-registry.ts
│       │   └── __tests__/
│       ├── app/                     [A]
│       │   ├── registry.ts  texts.ts  theme.css  brand/
│       │   ├── routes/                   URL → página (montadas en /t/$tenantId)
│       │   └── features/
│       │       └── appointments/         mismo nombre que el módulo del back
│       │           ├── pages/  components/  hooks/
│       │           └── __tests__/
│       └── gen/                     [G]  cliente API, tipos, seguridad por ruta, routeTree
│
├── api-spec/
│   ├── platform.yaml                [P]
│   ├── app.yaml                     [A]
│   └── openapi.gen.yaml             [G]  los dos unidos; de aquí leen todos los generadores
│
├── docs/
│   ├── platform/                    [P]  adr/  designs/  runbooks/  patterns.md
│   └── app/                         [A]  problem.md  spec.md  glossary.md  modules/  adr/
│
├── infra/                           [P]  db/ (roles y extensiones)  deploy/ (Kamal)
├── .github/workflows/               [P]  ci.yml  release.yml
├── .claude/skills/                  [P]
├── .vscode/settings.json            [P]  anidado de ficheros (_test.go bajo su fichero)
│
├── AGENTS.md                        [P]  reglas para cualquier agente
├── APP.md                           [A]  qué es esta app y sus reglas
├── CLAUDE.md                        [P]  @AGENTS.md + @APP.md
├── README.md  mise.toml  compose.yaml  .env.example    [C]
├── secrets.env  .sops.yaml          [A]
├── .template-paths                  [P]  rutas de plataforma que el CI de la app vigila
├── .template-version                [A]  commit de la plantilla del último sync
└── renovate.json  .editorconfig  .gitignore  LICENSE
```

Dentro de un módulo: `domain/` es lo que es verdad en el negocio; `usecases/` lo que se puede hacer y qué necesita (`ports.go`); `adapters/<tecnología>/` cómo se habla con el exterior. Un adaptador nuevo es una carpeta nueva al lado de las otras, sin tocar `domain/` ni `usecases/`. Los tests unitarios van junto a su fichero (Go lo exige); integración, arquitectura y BD, en `api/tests/`. En el front, los tests van agrupados en `__tests__/` por funcionalidad.

## Decisiones de estructura

- **Module path neutro:** `chaskicode.dev/saas`. Es una etiqueta, no una URL que tenga que resolver: igual en todas las apps y nunca se renombra, para que los ficheros de plataforma sean idénticos en la plantilla y en cada app. La identidad de cada app (nombre, imagen, base de datos) va en configuración.
- **Sin `internal/` ni `cmd/`:** el backend es un único binario que nadie importa; quitarlos ahorra dos niveles.
- **Migraciones separadas** (`platform/migrations` y `app/migrations`), cada una con su tabla de versiones de goose; `api migrate` aplica primero plataforma. Una migración de app puede referenciar tablas de plataforma, nunca al revés. Así una migración nueva de la plantilla no choca con la numeración de la app.
- **Contrato separado** (`platform.yaml` y `app.yaml`) y unido en `openapi.gen.yaml` dentro de `mise run generate`.
- **Un único punto de enganche por lado:** `api/app/app.go` (módulos y roles) y `web/src/app/registry.ts` (nombre, logo, menú). La plataforma no importa nada más de `app/`, comprobado por tests de arquitectura.
- **Mismo nombre de funcionalidad en back y front:** `api/app/modules/<x>` ↔ `web/src/app/features/<x>`.
- **Sincronización con la plantilla:** la app se crea clonando la plantilla (no con el botón "Use this template" de GitHub, que crea una historia sin ancestro común) y recibe mejoras con un remoto `template` y `git merge`. El CI de la app falla si cambian las rutas de `.template-paths` respecto a `.template-version`.
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

