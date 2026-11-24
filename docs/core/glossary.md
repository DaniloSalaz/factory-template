# Glosario

- **core**: la zona de la plantilla (antes «plataforma»): `api/core/`, `web/src/core/`, `api-spec/core.yaml` y `docs/core/`. Una app no la edita; llega por sincronización. Su contraparte es **app** (lo de cada app) y **gen** (lo generado). Nombre decidido en [ADR 0006](adr/0006-zona-core.md).
- **Módulo**: unidad de negocio autocontenida en `api/core/<m>` (salvo `kit`); solo expone `module.go`.
- **Dominio**: tipos y reglas de negocio del módulo, sin dependencias de infraestructura.
- **Caso de uso**: operación de la aplicación en `usecases/`; orquesta dominio y puertos.
- **Puerto**: interfaz definida en `usecases/` que describe lo que el caso de uso necesita del exterior (p. ej. `DBChecker`).
- **Adaptador**: implementación de un puerto o de una entrada (HTTP, Postgres).
- **Composition root**: `api/main.go`, el único lugar donde se cablean dependencias. La app se engancha solo desde `api/app/app.go` (`app.Roles`, `app.Modules`), que `main.go` llama.
- **Contrato**: `api-spec/core.yaml` (plantilla) y `api-spec/app.yaml` (app), fuente de verdad de la API HTTP; `mise run generate` los une en `api-spec/openapi.gen.yaml`, del que leen los generadores.
- **Liveness (`/healthz`)**: el proceso está vivo. **Readiness (`/readyz`)**: puede recibir tráfico (sus dependencias responden).
- **Problem details**: formato de error HTTP de RFC 9457 (`application/problem+json`).
