# AGENTS.md — platform/kit/db

Internos de las transacciones, la RLS y las migraciones. Se suman a los del `AGENTS.md` de la raíz y de `platform/kit/`. Runbook: `docs/platform/runbooks/database.md`.

## Transacciones y RLS
- `db.Independent` solo para escrituras que deben sobrevivir al rollback de quien llama (hoy, la auditoría de `authz.denied`).
- Ningún fichero Go fuera de `platform/kit/db` (y de los tests de esquema de `api/tests/db`) llama a `set_config` ni nombra las variables de RLS (`[rls-vars-only-in-db]`, `mise run arch`).
- Los errores `TN0xx` de las funciones de la BD se traducen con `db.MapError`.

## Migraciones en el binario
- Las migraciones (`api/platform/migrations` y `api/app/migrations`) van dentro del binario; el despliegue ejecuta `api migrate` (plataforma primero, app después) antes de cambiar de versión.
- La versión del binario (`-X main.buildVersion`) sale en los logs y en `/healthz`.

## Tests
- Los tests de integración de este paquete se quedan junto a su código porque usan sus propias tablas de ejemplo (`testdata/migrations/`), las mismas de `docs/platform/patterns.md`.
