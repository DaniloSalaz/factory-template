# factory-template

Plantilla base de la que nacen los SaaS: monolito modular en Go, contrato OpenAPI 3.1,
PostgreSQL 18 y frontend React + TypeScript + Vite. Las convenciones están en
[AGENTS.md](AGENTS.md); lo propio de cada app, en [APP.md](APP.md).

## Proyecto nuevo
Empieza por [docs/app/README.md](docs/app/README.md): problema → especificación → diseño de
módulo → implementación.

## Requisitos
- [mise](https://mise.jdx.dev) (instala Go, Node, sqlc, goose, oapi-codegen, golangci-lint, sops, age).
- Docker (Postgres local y tests de integración).

## Arranque
```sh
mise install && mise run hooks:install   # herramientas y hook de pre-commit (rechaza commits en main)
mise run up        # Postgres 18 (en un volumen nuevo crea roles y extensiones), Mailpit y LGTM
mise run migrate   # migraciones de plataforma y después de app, como app_owner, con `api migrate`
mise run dev       # API en http://localhost:8080, como app_runtime (descifra secrets.env)
cd web && npm run dev   # front en http://localhost:5173, con proxy de /auth y /api a la API
```

`mise run mail` abre Mailpit, donde llegan las invitaciones de desarrollo
([email](docs/core/runbooks/email.md)); `mise run obs`, Grafana local con trazas,
métricas y el dashboard ([observabilidad](docs/core/runbooks/observability.md)).
`curl localhost:8080/readyz` comprueba que la API y la BD responden. `mise tasks` lista
todas las tareas.

`mise run dev` necesita tu clave age para descifrar `secrets.env` (`SOPS_AGE_KEY_FILE`,
o la ruta por defecto de sops). El login con WorkOS se configura y se prueba como
explica [docs/core/runbooks/auth.md](docs/core/runbooks/auth.md).

La API conecta como `app_runtime`, sujeto a row-level security; las migraciones, como
`app_owner`. `MIGRATIONS_DATABASE_URL` equivale a acceso total a los datos: nunca se la
des a la API. Si tu volumen de Postgres es anterior a los roles, recréalo una vez con
`docker compose down -v` (o ejecuta `mise run db:bootstrap`). Detalles en
[docs/core/runbooks/database.md](docs/core/runbooks/database.md).

`/healthz` indica que el proceso vive; `/readyz` devuelve 200 si la BD responde y 503
(`application/problem+json`) si no.

## Zonas
Detalle en [docs/core/estructura.md](docs/core/estructura.md).
- `core/` — de la plantilla, no se toca desde una app: `api/core/`, `web/src/core/`, `api-spec/core.yaml`, `docs/core/`.
- `app/` — de cada app: `api/app/`, `web/src/app/`, `api-spec/app.yaml`, `docs/app/`, `APP.md`.
- `gen/` — generado con `mise run generate`, nunca a mano: `api/gen/`, `web/src/gen/`, `api-spec/openapi.gen.yaml`.
- Enganches de la app: `api/app/app.go` (módulos y roles) y `web/src/app/registry.ts` (marca y menú).

## Configuración
Los valores de desarrollo están en `[env]` de `mise.toml`. Para cambiarlos, copia
`.env.example` a `.env`. Los secretos (`WORKOS_API_KEY`) van cifrados con SOPS + age en
`secrets.env` (`sops edit secrets.env`; la clave pública está en `.sops.yaml`). mise no
descifra ficheros dotenv, así que `mise run dev` usa `sops exec-env`.

## Decisiones
- [ADR 0001 — Stack y arquitectura](docs/core/adr/0001-stack-y-arquitectura.md)
- [ADR 0002 — MFA opcional, exigida por rol](docs/core/adr/0002-mfa-opcional-por-rol.md)
- [ADR 0003 — Invariante del owner](docs/core/adr/0003-invariante-owner.md)
- [ADR 0004 — Telemetría sin datos personales](docs/core/adr/0004-telemetria-y-datos-personales.md)
- [ADR 0005 — Estructura plataforma / app / gen](docs/core/adr/0005-estructura-plataforma-app-gen.md)
