# factory-template

Plantilla base de la que nacen los SaaS: monolito modular en Go, contrato OpenAPI 3.1,
PostgreSQL 18 y frontend React + TypeScript + Vite. Las convenciones están en [AGENTS.md](AGENTS.md).

## Requisitos
- [mise](https://mise.jdx.dev) (instala Go, Node, sqlc, goose, oapi-codegen, golangci-lint, sops, age).
- Docker (Postgres local y tests de integración).

## Arranque
```sh
mise install
mise run hooks:install   # hook de pre-commit: rechaza commits en main
mise run up        # Postgres 18; en un volumen nuevo crea roles y extensiones
mise run migrate   # migraciones goose, como app_owner
mise run dev       # API en http://localhost:8080, como app_runtime (descifra secrets.env)
curl localhost:8080/readyz
cd web && npm run dev   # front en http://localhost:5173, con proxy de /auth y /api a la API
```

`mise run dev` necesita tu clave age para descifrar `secrets.env` (`SOPS_AGE_KEY_FILE`,
o la ruta por defecto de sops). El login con WorkOS se configura y se prueba como
explica [docs/runbooks/auth.md](docs/runbooks/auth.md).

La API conecta como `app_runtime`, sujeto a row-level security; las migraciones, como
`app_owner`. `MIGRATIONS_DATABASE_URL` equivale a acceso total a los datos: nunca se la
des a la API. Si tu volumen de Postgres es anterior a los roles, recréalo una vez con
`docker compose down -v` (o ejecuta `mise run db:bootstrap`). Detalles en
[docs/runbooks/database.md](docs/runbooks/database.md).

`/healthz` indica que el proceso vive; `/readyz` devuelve 200 si la BD responde y 503
(`application/problem+json`) si no.

## Tareas
`mise tasks` lista todas: `up`, `down`, `dev`, `generate`, `migrate`, `test`, `lint`, `fmt`.

## Configuración
Los valores de desarrollo están en `[env]` de `mise.toml`. Para cambiarlos, copia
`.env.example` a `.env`. Los secretos (`WORKOS_API_KEY`) van cifrados con SOPS + age en
`secrets.env` (`sops edit secrets.env`; la clave pública está en `.sops.yaml`). mise no
descifra ficheros dotenv, así que `mise run dev` usa `sops exec-env`.

## Decisiones
- [ADR 0001 — Stack y arquitectura](docs/adr/0001-stack-y-arquitectura.md)
