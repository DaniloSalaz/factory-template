# gen/

Código generado. **No se edita a mano**: se regenera con `mise run generate`.

- `openapi/<módulo>/` — servidor y modelos desde `api-spec/openapi.gen.yaml` (oapi-codegen, uno por `codegen.yaml` de módulo).
- `sqlc/<módulo>/` — acceso a datos desde `core/<módulo>/queries/` y `app/modules/<módulo>/queries/` (sqlc).
