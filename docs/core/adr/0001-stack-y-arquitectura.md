# ADR 0001 — Stack y arquitectura

- Estado: aceptado
- Fecha: 2026-11-20

## Contexto
Desarrollador único que quiere lanzar varios SaaS desde una plantilla común, con tipado
estricto, poco mantenimiento operativo y un contrato HTTP explícito entre back y front.

## Decisión
- **Monolito modular** en Go. Cada módulo (`api/core/<m>`) tiene `module.go`
  (API pública), `domain/`, `usecases/` (casos de uso + puertos), `adapters/http`,
  `adapters/postgres` y `queries/`. El dominio no depende de infraestructura y los módulos
  no importan el interior de otros. La regla del dominio la vigila depguard.
- **HTTP**: `net/http` con el ServeMux de la stdlib; servidor strict generado por
  oapi-codegen desde **OpenAPI 3.1** (`api-spec/openapi.yaml`), que es la fuente de verdad.
  El cliente TS se genera con orval. Errores en formato **RFC 9457**.
- **Datos**: PostgreSQL 18, pgx/v5, sqlc para queries tipadas, goose para migraciones SQL.
- **Colas**: River (sobre Postgres) cuando haga falta; evita otra pieza de infraestructura.
- **Secretos**: SOPS + age.
- **Frontend**: React + TypeScript (estricto) + Vite, Biome para lint y formato.
- **Tooling**: mise para versiones y tareas; versiones fijadas, nunca `latest`.
- **Observabilidad**: `log/slog` en JSON a stdout.

## Consecuencias
- Un único binario y una única BD: despliegue y operación simples; los límites entre
  módulos se mantienen por convención y lint, no por red.
- Todo cambio de API pasa por el contrato y la generación de código, lo que obliga a
  mantener el spec al día.
- Extraer un módulo a servicio más adelante es posible porque sus dependencias entran por
  puertos y constructores.
