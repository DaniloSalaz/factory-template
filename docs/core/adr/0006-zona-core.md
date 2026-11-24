# ADR 0006 — La zona de la plantilla se llama core

- Estado: aceptado
- Fecha: 2026-11-24

## Contexto
El [ADR 0005](0005-estructura-plataforma-app-gen.md) dividió cada zona del repo en tres
partes y llamó **plataforma** (`platform/`) a la que viene de la plantilla. Esa parte es
el núcleo que todas las apps comparten y que algún día se publicará como una librería
versionada de la que dependan, en vez de llegarles por `git merge`.

«Plataforma» no describe bien ese papel: sugiere un producto o un servicio desplegado
(una «plataforma SaaS»), no un núcleo de código que se importa, y choca con otros usos
de la palabra en el mismo repo (las plataformas `linux/amd64` y `linux/arm64` de la
imagen, `--platform` de Docker y Trivy). Además, cuanto más tarde se cambie, más apps
nacidas de la plantilla tendrán que hacer el mismo cambio al sincronizar.

## Decisión
La zona de la plantilla pasa a llamarse **core**, en todas partes donde su nombre es
nuestro:

- Carpetas: `api/core/`, `web/src/core/`, `docs/core/` y `api-spec/core.yaml`, movidas
  con `git mv` para conservar la historia. Imports `chaskicode.dev/saas/core/...` y alias
  `@/core/...`.
- Herramientas: `specmerge -core`, la tarea `migration:new:core` y el bucle de
  `generate` sobre `core/*/codegen.yaml`.
- Reglas y tests: `[core-imports-app]`, `[core-migrations-alone]`, `[core-no-modules]`,
  `[contract-core-owned]`, `TestCoreMigrationsApplyAlone`, `TestCoreImportsApp`, y los
  identificadores de Go y TS (`authz.CorePermissions`, `testkit.CoreFromSource`,
  `CoreErrorBoundary`, `useCore`…). El conjunto de migraciones se llama `core`
  (`MigrationSet.Name`, que solo aparece en logs y errores).
- Documentación: «plataforma» pasa a «core» donde se refiere a esta zona.

El comportamiento no cambia. Se queda con el nombre antiguo lo que ya está guardado
fuera del código o lo usa alguien de fuera:

- La tabla de versiones de goose de core sigue siendo `goose_db_version`: la tienen las
  bases de datos existentes y la protege `TestSet_MatchesCoreFromSource`. Ninguna
  migración fusionada cambia, tampoco sus comentarios (son inmutables), ni ningún objeto
  de la base de datos.
- Las claves de navegador `platform.inviteToken` (`sessionStorage`) y
  `platform.lastTenantId` (`localStorage`): cambiarlas perdería el token de una
  invitación a medio aceptar o el último tenant de quien ya usa la app.
- Lo que no es nuestro: las plataformas de la imagen Docker en `release.yml`, el módulo
  `github.com/containerd/platforms` de `go.mod` y `go.sum`, «supported platforms» en un
  comentario sobre `crypto/rand`.
- El texto de los ADR anteriores, que se cambia solo en rutas e identificadores: donde
  digan «plataforma» refiriéndose a la zona, se lee «core». El fichero del ADR 0005
  conserva su nombre.
- La marca **[P]** de `estructura.md` y `AGENTS.md`, ahora «de core»: renombrarla tocaría
  cada línea del árbol sin aclarar nada.

## Alternativas descartadas
- **Dejar `platform`**: el coste de cambiar solo crece con cada app nacida de la
  plantilla, y el nombre seguiría diciendo algo distinto de lo que es.
- **`template` / `plantilla`**: la plantilla es el repo entero (incluidos los ficheros
  compartidos y los ejemplos de `app/`), no solo esta zona.
- **`kit`**: ya es el nombre de `core/kit`, las piezas transversales sin negocio; la
  zona contiene además módulos con negocio (`identity`, `health`).
- **`base`, `framework`, `lib`**: `base` no dice nada; `framework` promete inversión de
  control que no hay (la app llama a core y core solo la lee por un enganche); `lib`
  describe la forma futura de distribución, no la de hoy.
- **Renombrar también lo persistente** (tabla de goose, claves de navegador) con una
  migración o un periodo de doble lectura: añade riesgo y código a cambio de coherencia
  en nombres que nadie lee.

## Consecuencias
- Cada app nacida de la plantilla recibe el cambio en su próxima sincronización: un
  `git merge` con renombrados de carpeta, que git sigue; los imports de `app/` hacia
  `platform/...` tienen que cambiarse a `core/...` en ese mismo merge.
- Los mensajes de las reglas nombran `core/`, así que una app que no haya sincronizado
  verá en sus fallos de CI los identificadores antiguos hasta que lo haga.
- Quedan las excepciones de arriba, cada una con su motivo junto al código cuando no es
  evidente.
