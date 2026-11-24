# AGENTS.md — core/kit/db

Internos de las migraciones y de los tests de este paquete. Se suman a los del `AGENTS.md` de la raíz (que recoge `db.Independent`, `set_config` / `[rls-vars-only-in-db]` y `db.MapError`, porque afectan también al código de fuera) y de `core/kit/`. Runbook: `docs/core/runbooks/database.md`.

## Migraciones en el binario
- Las migraciones (`api/core/migrations` y `api/app/migrations`) van dentro del binario; el despliegue ejecuta `api migrate` (core primero, app después) antes de cambiar de versión.
- La versión del binario (`-X main.buildVersion`) sale en los logs y en `/healthz`.

## Tests
- Los tests de integración de este paquete se quedan junto a su código porque usan sus propias tablas de ejemplo (`testdata/migrations/`), las mismas de `docs/core/patterns.md`.
