# AGENTS.md — platform/kit

Reglas de las piezas transversales de la plantilla. Se suman a las del `AGENTS.md` de la raíz; las de `db/`, en `db/AGENTS.md`.

## Dependencias
- `platform/kit` no importa ningún módulo de plataforma (`platform/<m>`, depguard `[platform-no-modules]`).
- `platform/kit/apperr` y `platform/kit/authctx` solo importan la stdlib (`[apperr-stdlib-only]`, `[authctx-stdlib-only]`).

## Configuración (`config/`)
- Fuera de development (`staging`, `production`): todas las URL y orígenes del navegador y del proveedor de identidad son `https`, y el SMTP es obligatorio; config lo valida al arrancar.
- Una opción solo para desarrollo (como `SMTP_TLS_SKIP_VERIFY`) se rechaza fuera de él.
