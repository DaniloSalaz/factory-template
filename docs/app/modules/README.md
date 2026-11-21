# Diseño de módulos

Un fichero por módulo, con el mismo nombre que su carpeta: `docs/app/modules/<m>.md` ↔ `api/app/modules/<m>` ↔ `web/src/app/features/<m>`.

## Qué lleva cada diseño
- **Problema**: qué parte de [`spec.md`](../spec.md) resuelve el módulo.
- **Casos de uso**: una acción por línea, con quién la hace.
- **Contrato**: operaciones de `api-spec/app.yaml` (método, ruta, `operationId`), con el `tag` del módulo.
- **Tablas y RLS**: tablas, su alcance (`tenant`, `own`, `access`) y los permisos de `app_runtime` (`docs/platform/patterns.md`).
- **Permisos**: qué permisos nuevos necesita y qué roles de `app.Roles` los tienen.
- **Criterios de aceptación**: comprobables, uno por línea.
