# Glosario

- **Módulo**: unidad de negocio autocontenida en `api/platform/<m>` (salvo `kit`); solo expone `module.go`.
- **Dominio**: tipos y reglas de negocio del módulo, sin dependencias de infraestructura.
- **Caso de uso**: operación de la aplicación en `usecases/`; orquesta dominio y puertos.
- **Puerto**: interfaz definida en `usecases/` que describe lo que el caso de uso necesita del exterior (p. ej. `DBChecker`).
- **Adaptador**: implementación de un puerto o de una entrada (HTTP, Postgres).
- **Composition root**: `api/main.go`, el único lugar donde se cablean dependencias.
- **Contrato**: `api-spec/openapi.yaml`, fuente de verdad de la API HTTP.
- **Liveness (`/healthz`)**: el proceso está vivo. **Readiness (`/readyz`)**: puede recibir tráfico (sus dependencias responden).
- **Problem details**: formato de error HTTP de RFC 9457 (`application/problem+json`).
