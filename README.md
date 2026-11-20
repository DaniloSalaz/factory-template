# factory-template

Plantilla base de la que nacen los SaaS: monolito modular en Go, contrato OpenAPI 3.1,
PostgreSQL 18 y frontend React + TypeScript + Vite. Las convenciones están en [AGENTS.md](AGENTS.md).

## Requisitos
- [mise](https://mise.jdx.dev) (instala Go, Node, sqlc, goose, oapi-codegen, golangci-lint, sops, age).
- Docker (Postgres local y tests de integración).

## Arranque
```sh
mise install
mise run up        # Postgres 18
mise run migrate   # migraciones goose
mise run dev       # API en http://localhost:8080
curl localhost:8080/readyz
```

`/healthz` indica que el proceso vive; `/readyz` devuelve 200 si la BD responde y 503
(`application/problem+json`) si no.

## Tareas
`mise tasks` lista todas: `up`, `down`, `dev`, `generate`, `migrate`, `test`, `lint`, `fmt`.

## Configuración
Los valores de desarrollo están en `[env]` de `mise.toml`. Para cambiarlos, copia
`.env.example` a `.env`. Los secretos de entornos reales irán cifrados con SOPS + age en
`config/` (pon tu clave pública en `.sops.yaml`).

## Decisiones
- [ADR 0001 — Stack y arquitectura](docs/adr/0001-stack-y-arquitectura.md)
