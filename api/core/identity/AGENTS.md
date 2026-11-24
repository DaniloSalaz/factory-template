# AGENTS.md — identity

Reglas internas del módulo `identity` (sesiones, tenants, miembros, invitaciones, MFA, email). Se suman a las del `AGENTS.md` de la raíz, que recoge las de email porque también valen para un módulo de app que envíe correo. Diseño: `docs/platform/designs/auth-y-tenancy.md`; ADR 0002 y 0003 en `docs/platform/adr/`.

## Membresías
- Las membresías no se escriben directamente: `app_runtime` solo tiene `SELECT` sobre `memberships`. Se crean con `create_tenant` y `accept_invitation`, y su rol cambia o se borran solo con `change_member_role`, `remove_member` y `leave_tenant`, que bloquean el tenant y nunca lo dejan sin owner (ADR 0003).
- La jerarquía (quién gestiona a quién) se decide en `domain` con la inclusión de permisos del catálogo, nunca comparando nombres de rol; la BD solo comprueba que los roles en los que se basó Go no cambiaron.
- Orden de bloqueo en la BD: fila del tenant → invitaciones → membresías. Quien escribe invitaciones bloquea antes el tenant (`FOR KEY SHARE`) y relee el rol del actor; las funciones de miembros lo bloquean `FOR UPDATE`. Otro orden puede interbloquearse con una degradación (`40P01`).

## Invitaciones
- El token de una invitación (y su hash) no va en ningún log, respuesta ni auditoría; solo el adaptador de desarrollo de `InvitationSender` (`adapters/devsender`) escribe el enlace, y no arranca fuera de desarrollo.
- El envío se hace después del commit y su fallo solo se registra (el remedio es reenviar).

## MFA (ADR 0002)
- Opcional, exigida solo por los roles del catálogo que lo dicen.
- Una sesión solo tiene MFA por `sessions.mfa_auth_time`, que fija únicamente `mark_session_mfa` tras un `valid: true` del proveedor (y rota el token). Nunca se deduce del login ni de que el usuario tenga un factor.

## Tests
- Los `*_integration_test.go` de la raíz del módulo y de `adapters/smtpsender/` se quedan aquí porque importan el interior del módulo (`adapters/fakeidp`, `usecases`, `domain`); fuera, en `api/tests/`, romperían `[module-boundaries]`.
