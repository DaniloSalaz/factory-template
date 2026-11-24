# Documentación de la app

Punto de entrada de cada proyecto nuevo. Se trabaja en este orden; cada paso parte del anterior.

## Flujo
1. **Problema** → [`problem.md`](problem.md): qué se resuelve y para quién, antes de pensar en pantallas.
2. **Especificación** → [`spec.md`](spec.md): qué hace la app, sus reglas y sus datos.
3. **Diseño de módulo** → [`modules/<m>.md`](modules/README.md): uno por módulo de `api/app/modules/<m>`, con el mismo nombre.
4. **Generador** → crea el esqueleto del módulo (`api/tools/genmodule`, fase 2; hasta entonces, a mano como dice `AGENTS.md`).
5. **Implementación** → contrato en `api-spec/app.yaml`, migración, casos de uso y pantallas, según `AGENTS.md`.

## Qué va en cada fichero
| Fichero | Contenido |
|---|---|
| [`problem.md`](problem.md) | El problema, sin solución técnica |
| [`spec.md`](spec.md) | Usuarios, funcionalidades, reglas, datos, integraciones |
| [`glossary.md`](glossary.md) | Términos del negocio y su significado |
| [`modules/`](modules/README.md) | Diseño de cada módulo |
| [`adr/`](adr/README.md) | Decisiones propias de la app |

Lo de la plantilla (identidad, tenants, base de datos, despliegue) está en [`docs/core/`](../core/estructura.md).
