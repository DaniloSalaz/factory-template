# ADR 0005 — Estructura en zonas plataforma / app / gen

- Estado: aceptado
- Fecha: 2026-11-21

## Contexto
De esta plantilla nacen varias apps, y cada una tiene que seguir recibiendo sus mejoras
(seguridad, identidad, observabilidad) durante años. Antes de la reorganización, el código
de la plantilla y el de una app futura compartían carpetas (`api/internal/…`, una sola
carpeta de migraciones, un solo `openapi.yaml`): una app que añadiera un módulo, una tabla
o un endpoint tocaba los mismos ficheros que la plantilla, y cada sincronización acabaría
en conflictos. Tampoco había un límite que un agente pudiera leer para saber qué puede
tocar y qué no.

## Decisión
Cada zona del repo se divide en tres partes, con el detalle en
[`docs/platform/estructura.md`](../estructura.md):

- **`platform/`** — de la plantilla. Una app no lo edita; llega por sincronización
  (`api/platform/`, `web/src/platform/`, `api-spec/platform.yaml`, `docs/platform/`).
- **`app/`** — de cada app (`api/app/`, `web/src/app/`, `api-spec/app.yaml`, `docs/app/`,
  `APP.md`).
- **`gen/`** — generado, nunca a mano (`api/gen/`, `web/src/gen/`, `api-spec/openapi.gen.yaml`).

Y, para que esa separación se sostenga:

- **Module path neutro** `chaskicode.dev/saas`, igual en todas las apps y nunca
  renombrado: los ficheros de plataforma son idénticos byte a byte en la plantilla y en cada
  app. La identidad de cada app va en configuración.
- **Migraciones separadas**: `api/platform/migrations/` (tabla `goose_db_version`) y
  `api/app/migrations/` (tabla `goose_app_db_version`), con numeración propia; `api migrate`
  aplica primero plataforma. Una de app puede usar lo de plataforma, nunca al revés.
- **Contrato separado**: `api-spec/platform.yaml` y `api-spec/app.yaml`, unidos por
  `api/tools/specmerge` en `api-spec/openapi.gen.yaml`; `app.yaml` no puede redefinir nada de
  plataforma. Cada operación tiene un único tag, el nombre de su módulo, y cada módulo genera
  su propio paquete (`codegen.yaml`).
- **Un punto de enganche por lado**: `api/app/app.go` (`app.Roles`, `app.Modules`) y
  `web/src/app/registry.ts`. `main.go` y la plataforma no importan nada más de `app/`.
- **Tests no unitarios en `api/tests/`** (`arch/`, `db/`, `integration/<p>/`, `testkit/`);
  los unitarios, junto a su fichero. Un test de integración se queda junto a su código solo
  si usa algo no exportado, es de `package main` o importa el interior de su módulo.
- **Instrucciones en capas**: `AGENTS.md` en la raíz con lo que necesita cualquier sesión,
  `AGENTS.md` en las carpetas con reglas internas (`identity`, `kit`, `kit/db`) y `APP.md`
  para lo propio de la app.

Lo protegen reglas con identificador, que fallan en `mise run ci`: `[platform-imports-app]`,
`[module-boundaries]`, `[feature-boundaries]`, `[tests-not-in-prod]`,
`[platform-migrations-alone]`, `[contract-duplicate]`, `[contract-platform-owned]` y
`[contract-module-key]`, entre otras (lista completa en `estructura.md`).

## Alternativas descartadas
- **`internal/` y `cmd/`**: el backend es un único binario que nadie importa; añadían dos
  niveles de carpetas sin proteger nada que no protejan ya las reglas de arquitectura.
- **Module path por app** (`github.com/<owner>/<app>`): cada import de plataforma cambiaría
  en cada app y toda sincronización tocaría cientos de ficheros.
- **Un único conjunto de migraciones**: la numeración de la plantilla y la de la app
  chocarían en cuanto las dos añadieran una.
- **Un único `openapi.yaml`**: cada endpoint de la app sería un conflicto con la plantilla.
- **Botón «Use this template» de GitHub**: crea una historia sin ancestro común y hace
  imposible `git merge` desde la plantilla; las apps se crean clonando y con un remoto
  `template`.
- **Tests de integración junto a cada paquete**: mezclaban lo que necesita Docker con el
  código de producción y dejaban que helpers de test dependieran de la app desde
  `platform/`.

## Consecuencias
- Una app solo escribe en `app/` (y en los ficheros compartidos [C] de `estructura.md`); un
  cambio en `platform/` se hace en factory-template y llega por sincronización.
- La frontera es comprobable: las reglas de arquitectura fallan en CI con un mensaje que dice
  qué regla es y cómo arreglarlo.
- Hay más piezas de generación (`specmerge`, un `codegen.yaml` por módulo, dos conjuntos de
  migraciones) que mantener en la plantilla.
- Queda pendiente (fase 2): el generador de módulos (`api/tools/genmodule`),
  `.template-paths` / `.template-version` y la comprobación de deriva en el CI de cada app.
