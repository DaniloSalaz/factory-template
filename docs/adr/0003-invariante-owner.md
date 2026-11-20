# ADR 0003 — Todo tenant activo tiene al menos un owner, y lo garantiza la base de datos

- Estado: aceptado
- Fecha: 2026-11-20

## Contexto
El paso 5a añade la gestión de miembros: cambiar el rol, expulsar y salir del tenant.
Las tres pueden quitar el último owner. Comprobarlo en Go no basta. Dos owners que se
degradan el uno al otro a la vez leen cada uno al otro como owner, y los dos cambios
pasan. Además, hasta ahora `app_runtime` tenía `UPDATE (role)` y `DELETE` directos sobre
`memberships`: cualquier consulta escrita a mano podía saltarse la comprobación.

El diseño también exige que una decisión de la jerarquía no se tome sobre un rol que ya
no es el actual (diseño, 5.4: "lo releen y bloquean dentro de su transacción").

## Decisión
- **Las tres escrituras van por funciones `SECURITY DEFINER`** (`change_member_role`,
  `remove_member`, `leave_tenant`, migración 00005), y `app_runtime` pierde `UPDATE (role,
  updated_at)` y `DELETE` sobre `memberships`. Ya no hay otra forma de cambiar quién es
  owner. Las membresías solo se crean con `create_tenant` (con el creador como owner) y
  `accept_invitation`.
- **Cada función bloquea la fila del tenant** (`SELECT … FOR UPDATE`, en el auxiliar
  `lock_tenant_members`) antes de contar owners. Así todas las que tocan los roles de un
  tenant se ejecutan una detrás de otra, y la segunda cuenta lo que dejó la primera.
  Rechazan con `TN061` (`409 membership.last_owner`) el cambio que dejaría el tenant sin
  owner. Si el tenant no está activo, `TN014`.
- **El tenant es siempre `app.tenant_id`**, nunca un parámetro, y un usuario que no es
  miembro de ese tenant da `TN060` (`404 membership.not_found`). El actor sale de
  `app.user_id`.
- **Go decide la jerarquía; la BD comprueba que sigue valiendo.** El caso de uso juzga con
  el rol del actor de la petición y el rol actual del destinatario, y pasa los dos a la
  función (`p_expected_actor_role`, `p_expected_member_role`). La función, ya con el
  bloqueo, vuelve a leer los dos roles y rechaza con `TN062` (`409 membership.changed`) si
  alguno cambió. La BD sigue sin evaluar permisos (diseño, principio 1): solo compara
  textos. El único nombre de rol que conoce es `owner`, que la plataforma reserva.
- Cada función audita su cambio en la misma transacción (`membership.role_changed` con los
  dos roles, `membership.removed`, `membership.left`), con el actor y el destinatario.

## Alternativas descartadas
- **Un disparador de restricción** (`CONSTRAINT TRIGGER … DEFERRABLE INITIALLY DEFERRED`
  sobre `memberships`) que compruebe al final de la transacción que queda un owner. Cubre
  cualquier escritura, también las de `app_admin`. Pero necesita el mismo bloqueo del
  tenant para no tener la carrera entre dos transacciones. Además, falla al hacer commit,
  lejos del caso de uso, con un error que Go tendría que reconocer en el commit. Y no
  resuelve el segundo problema: comprobar que la decisión de la jerarquía no se tomó sobre
  un rol obsoleto. Las funciones ya hacían falta para eso y para no dar a `app_runtime`
  escritura directa.
- **Bloquear y releer en Go** (`SELECT … FOR UPDATE` del tenant desde el caso de uso, y
  después las escrituras directas). Deja la invariante en manos de que cada camino del
  código bloquee primero, y obliga a mantener la escritura directa de `app_runtime`.
- **Releer el rol del actor en Go además de en la BD.** Era redundante con `TN062`. Además,
  en la carrera entre dos owners, la petición perdedora recibía `403` o `409` según el
  orden de las lecturas dentro de las transacciones, aunque las dos peticiones se
  solaparan.

## Consecuencias
- Los roles de un tenant cambian uno detrás de otro. Mientras tanto, aceptar una
  invitación de ese tenant espera: su clave foránea pide `KEY SHARE` sobre la misma fila.
  Son transacciones de milisegundos.
- `app_admin` no pasa por las funciones: un comando de administración puede saltarse la
  invariante, y el diseño ya le exige confirmación explícita al deshabilitar o borrar al
  último owner (5.5 y 5.8).
- La petición que pierde una carrera recibe `409`, y el cliente vuelve a leer y reintenta.
  Si la petición perdedora empieza cuando la ganadora ya ha confirmado, el cambio ya surte
  efecto en ella: degradada, la jerarquía la rechaza (`403 membership.target_forbidden`);
  expulsada, el middleware de tenant la rechaza (`403 auth.tenant_forbidden`).
- `lock_tenant_members` es `SECURITY INVOKER` y no tiene `EXECUTE` para nadie: solo la
  llama su dueño, `app_security`, desde las funciones anteriores.
- Un usuario con alcance `own` ya no borra su propia membresía con un `DELETE` directo:
  salir del tenant es `leave_tenant()`. Las políticas `own_scope` y `own_write` de
  `memberships` siguen como segunda capa.
