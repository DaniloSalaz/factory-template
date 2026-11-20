# Diseño: autenticación y tenancy

Estado: v4, tras tres revisiones sin contexto · Fase 1 de factory-template

## 1. Objetivo

Dar a todas las apps de la fábrica una identidad, una separación por cliente (tenant) y unos permisos comunes, de forma que:

- un usuario de un tenant **nunca** pueda leer ni modificar datos de otro, aunque un desarrollador o un agente olvide un filtro;
- dentro de un tenant, un usuario con alcance restringido (el cliente de un despacho) nunca vea datos ni personas de otros, por el mismo motivo;
- cambiar de proveedor de identidad sea cambiar un adaptador más una migración controlada, nunca perder usuarios;
- cada app defina sus propios roles sin tocar la plataforma.

### Frontera de confianza

RLS protege contra **omisiones** del código. No protege contra una aplicación comprometida: `app_runtime` decide qué usuario y qué tenant fija en cada transacción. Esa frontera la protegen la sesión, los middlewares, los tests y la revisión de código.

### Principios que resuelven la mayoría de los casos

1. **Solo el código Go decide permisos.** La BD guarda roles como texto, aplica el aislamiento y ofrece primitivas de acceso, pero nunca evalúa permisos.
2. **Cada función `SECURITY DEFINER` revalida por sí misma todo lo que protege.** Nunca confía en que otra llamada anterior de la misma transacción ya lo hizo.
3. **Lo que no hace falta, no existe.** Se han eliminado la vinculación automática por email, la inactividad por rol y la revalidación de quien invitó (sustituida por revocación, sección 5.6).

## 2. Decisiones

| Decisión | Elegido | Descartado y por qué |
| --- | --- | --- |
| Proveedor de identidad | WorkOS AuthKit, detrás del puerto `IdentityProvider` | Zitadel: alternativa si un cliente exige datos en la UE |
| Fuente de verdad de tenants, membresías y roles | Postgres propio | Organizaciones y roles del proveedor |
| Identidades externas | `user_identities`, sin vinculación automática por email | Vincular por email verificado: permite robar cuentas |
| Sesión | BFF: cookie `HttpOnly` opaca, sesión en Postgres | Tokens en el navegador |
| Elección del tenant | Cabecera `X-Tenant-ID` | Subdominio |
| Aislamiento | RLS en todas las tablas con `tenant_id` y en `tenants` (por su `id`), con alcance declarado y comprobado por test | Alcance opcional |
| Escritura de tenants y membresías | Solo con funciones `SECURITY DEFINER` (`create_tenant_with_owner`, `accept_invitation`); `app_runtime` no tiene `INSERT` en ninguna de las dos ni `DELETE` en `tenants`, y en `tenants` solo actualiza `name` | `INSERT` directo filtrado por RLS: un fallo del código podría crear un tenant sin owner o una membresía sin invitación |
| Rol `owner` | Reservado por la plataforma; solo un owner concede owner | Tratarlo con la regla general de inclusión |
| Invitaciones de quien pierde permisos | Se revocan al degradar, expulsar o deshabilitar a quien invitó | Revalidar a quien invitó al aceptar |
| MFA | Por rol, con la hora real de autenticación. WorkOS no informa de la MFA: se exige a todos en el panel (`WORKOS_MFA_REQUIRED`) y cada login es una autenticación nueva; cuando caduca la de un rol, login completo (5.2) | Step-up con rotación del token: WorkOS no permite exigir MFA en la reautenticación ni saber si la hubo (sección 13) |
| Locales | Fuera de esta versión | — |
| Clientes del despacho | Usuarios con cuenta y rol `client` (alcance `own`) | Enlace mágico |

## 3. Requisitos de despliegue

- Front y API de producción comparten sitio: `app.x.com` y `api.x.com`.
- Las previsualizaciones van en **otro dominio registrable** (`x-preview.com`), con su propia API y base de datos.
- `ALLOWED_ORIGINS` de producción: lista cerrada, sin comodines.
- Elimina los registros DNS de `x.com` que apunten a servicios que ya no uses.
- La API solo es accesible por Cloudflare Tunnel, en staging y en producción; la IP del cliente se toma de `CF-Connecting-IP` (`TRUSTED_CLIENT_IP_HEADER`, que lo tiene por defecto en esos dos entornos), nunca de `X-Forwarded-For`.
- Las extensiones de Postgres (`citext`, `pgcrypto`, `pg_trgm`) se instalan en un esquema propio, `extensions`.

## 4. Modelo de datos

Claves primarias UUIDv7; todas las tablas con `created_at`, y las mutables con `updated_at`.

### 4.1 Tablas de identidad, sin RLS

Sin `tenant_id` ni RLS. Solo las consultas del módulo `identity` las tocan; un test de arquitectura lo comprueba sobre sqlc (no detecta consultas escritas a mano con pgx: eso queda para la revisión de código).

- **users**: `id`, `email` (citext, único), `email_verified`, `display_name`, `last_login_at`, `disabled_at`. Un **usuario de sistema** reservado (UUID fijo `00000000-0000-7000-8000-000000000001`, sin identidades ni login) para los trabajos de sistema. Se crea deshabilitado, con `email = 'system@system.invalid'` (dominio reservado, RFC 2606) y `email_verified = false`: cualquier función que exija un usuario activo y verificado lo rechaza sin caso especial.
- **user_identities**: `id`, `user_id`, `provider`, `subject`, `email_at_link`, `linked_at`. Único `(provider, subject)`.
- **sessions**: `id`, `token_hash` (único), `user_id`, `auth_time`, `mfa_auth_time` (nullable), `last_seen_at`, `idle_expires_at`, `absolute_expires_at`, `ip_prefix`, `user_agent`, `provider_session_id` (nullable), `revoked_at`.

### 4.2 Tablas de plataforma bajo RLS

Todas con RLS, la política estándar y su alcance declarado. `tenants` no tiene `tenant_id`: su política compara su propio `id` con `app.tenant_id`, así que la aplicación solo lee y renombra el tenant actual. Los flujos sin tenant (middleware de tenant, `/me`, `POST /tenants`, aceptar invitación, trabajos de sistema) leen `tenants` a través de las funciones `SECURITY DEFINER` (sección 7.4).

| Tabla | Alcance | Qué ve un usuario con alcance `own` |
| --- | --- | --- |
| **tenants**: `id`, `slug` (único), `name`, `status` (`active` \| `suspended`) | `tenant-shared`, con `tenant_isolation` sobre `id` | El tenant actual |
| **memberships**: `id`, `tenant_id`, `user_id` (FK `ON DELETE RESTRICT`), `role`; único `(tenant_id, user_id)` | `own_scope`: `scope = 'tenant' OR user_id = app.user_id`; además `own_write` (sección 7.3) | Solo su propia membresía; puede borrarla (abandonar el tenant), pero no modificarla |
| **invitations**: `id`, `tenant_id`, `email` (citext), `role`, `token_hash`, `invited_by` (nullable, FK `ON DELETE SET NULL`), `expires_at`, `accepted_at`, `revoked_at`; único parcial `(tenant_id, email) WHERE accepted_at IS NULL AND revoked_at IS NULL` | `own_scope`: `scope = 'tenant'` | Nada |
| **audit_log**: `id`, `tenant_id` (nullable), `actor_user_id` (nullable, FK `ON DELETE SET NULL`), `action`, `target_type`, `target_id`, `metadata` (jsonb sin datos personales), `ip_prefix`, `created_at` | `own_scope`: `scope = 'tenant'` | Nada |

Todas las claves foráneas hacia `tenants` son `ON DELETE RESTRICT`: el borrado de un tenant queda fuera de esta versión (sección 12). `memberships.user_id` también es `RESTRICT`: el borrado de un usuario quita sus membresías explícitamente, publicando un evento por cada una (sección 5.8). `invitations.invited_by` pasa a `NULL` para conservar el historial.

`audit_log`:
- `app_runtime` no tiene `INSERT`, `UPDATE` ni `DELETE` directos; inserta con `record_audit()` y lee con las políticas (y el permiso `audit:read` en Go).
- Es inmutable **para la aplicación**: un fallo podría registrar eventos falsos, pero no borrar ni modificar los existentes.
- `ON DELETE SET NULL` funciona porque las acciones de las claves foráneas se ejecutan con permisos del dueño de la tabla.

### 4.3 Tablas de negocio de las apps

`tenant_id uuid NOT NULL REFERENCES tenants(id)`, índice que empieza por `tenant_id`, `ENABLE` y `FORCE ROW LEVEL SECURITY`, política estándar y alcance declarado (sección 7.3). Las **tablas de acceso** (como `case_access`) también declaran su alcance.

## 5. Flujos

### 5.1 Login

1. `GET /auth/login?return_to=<ruta>`. `return_to` se resuelve con un parser de URL contra `FRONTEND_ORIGIN` y el origen resultante debe ser exactamente ese.
2. Se crea un **intento** en una cookie `__Host-oauth_<state>` (`HttpOnly`, `Secure`, `SameSite=Lax`, 5 minutos, como mucho 3 vivas). El intento guarda: el verificador PKCE, `return_to`, el **propósito** (`login`) y la hora de inicio. Va sellado con HMAC-SHA256 sobre el `state` y el contenido, así que el navegador lo guarda pero no lo puede alterar ni mover a otro `state`. Redirige a AuthKit.
   - **Clave del sello**: se deriva de `WORKOS_API_KEY` con HMAC y una etiqueta fija (`factory-template/identity/oauth-intent/v1`). Así todas las réplicas comparten la misma clave sin un secreto más. Rotar la API key invalida los logins en curso (5 minutos como mucho), y quien tenga la API key podría forjar intentos, pero con ella ya controla el proveedor.
3. `GET /auth/callback?code&state`: localiza y borra la cookie de ese intento. **El propósito lo dice el intento, nunca un parámetro de la URL.** Canjea el código y obtiene `subject`, `email`, `email_verified`, la hora real de autenticación (`auth_time` del access token), el método y si es una suplantación. Si hubo MFA se decide como dice la 5.2.
4. Rechaza, registrando `auth.login_failed` con el motivo, y en este orden:
   - **suplantación** (`impersonation`): WorkOS indica que alguien (un administrador del panel) entró como el usuario. La suplantación para soporte está fuera de esta versión (sección 12): `403 auth.impersonation_refused`;
   - **autenticación antigua** (`stale_auth`): `auth_time` anterior al inicio del intento, con 60 s de margen de reloj. Cada login fuerza una autenticación nueva (5.2.1); si el proveedor no lo hizo, `auth_time` y la MFA deducida describirían otro login: `401 auth.stale_authentication`;
   - **email sin verificar** (`email_unverified`): `403 auth.email_unverified`;
   - métodos informados sin MFA cuando se exige (`mfa_missing`, 5.2.3).
5. Resuelve el usuario **sin escribir nada**:
   - Existe `user_identities(workos, subject)`: ese usuario.
   - No existe y el email no lo usa nadie: usuario nuevo.
   - No existe pero el email ya lo usa otro usuario: `409 auth.email_in_use`. Antes de rechazar se vuelve a leer la identidad: con `READ COMMITTED`, un primer login simultáneo de la misma identidad puede haberla confirmado entre las dos lecturas.
6. Rechaza si el usuario está deshabilitado. Hasta aquí no se ha escrito nada salvo, en caso de rechazo, `auth.login_failed`.
7. Crea el usuario y la identidad si son nuevos. Si dos primeros logins de la misma identidad compiten, el que choca con la unicidad repite la resolución una vez, en una transacción nueva, y encuentra la del ganador. Si el email verificado cambió en el proveedor y no lo usa otro usuario, actualiza `users.email` con su `email_verified`. Si lo usa otro usuario, conserva el email anterior **con `email_verified = false`**: el proveedor ya no lo respalda, y no debe abrir invitaciones dirigidas a esa dirección.
8. Revoca la sesión anterior del navegador, si había (evita la fijación de sesión).
9. Crea la sesión (32 bytes aleatorios; en la BD solo el hash SHA-256) con `auth_time` y, si hubo MFA, `mfa_auth_time`, ambos **con la hora que da el proveedor**, no con la del servidor, y la sesión de AuthKit (`sid`) para cerrarla en el logout.
10. Pone la cookie, redirige a `return_to` y registra `auth.login`.

Vincular otra identidad desde una sesión ya autenticada: fuera de esta versión. Migración entre proveedores: comando de administración, nunca en el login.

### 5.2 MFA y reautenticación

El step-up de versiones anteriores (reautenticar con MFA y rotar el token en la misma sesión) **no se puede construir sobre WorkOS**: AuthKit no permite exigir un factor MFA al reautenticar ni informa de si lo hubo (sección 13). En su lugar:

1. **Cada login es una autenticación nueva**: la URL de autorización lleva `max_age=0`, así que AuthKit pide credenciales aunque tenga una sesión abierta, y `auth_time` es la hora de ese login.
2. **MFA obligatoria en el proveedor** para todos los usuarios de las apps con roles que la exigen: en el panel de WorkOS, *Multi-factor auth* en *Required*, declarado a la app con `WORKOS_MFA_REQUIRED=true`.
3. **`mfa_auth_time`**:
   - si el access token trae los métodos de autenticación (`amr`, RFC 8176), se usan ellos: con `mfa`, `mfa_auth_time = auth_time`; sin `mfa` y con `WORKOS_MFA_REQUIRED=true`, el login se rechaza (`403 auth.mfa_required`, `auth.login_failed` con motivo `mfa_missing`). Hoy WorkOS no envía `amr`;
   - si no los trae, `mfa_auth_time = auth_time` solo con `WORKOS_MFA_REQUIRED=true` y un método de la **lista blanca**: los métodos que una prueba en staging ha demostrado que hacen que AuthKit pida el segundo factor. Lista actual: `Password`. Cualquier otro método, incluidos los desconocidos y los que WorkOS añada, deja `NULL`: WorkOS documenta que su MFA no se aplica a `SSO` y no dice nada de Magic Auth, passkeys ni OAuth. La MFA se **deduce de la configuración**, no la informa el proveedor; `docs/runbooks/auth.md` describe la comprobación manual de que `WORKOS_MFA_REQUIRED` coincide con el panel en cada entorno y cómo añadir un método a la lista.
4. **Arranque**: si algún rol del catálogo requiere MFA y `WORKOS_MFA_REQUIRED=false`, la API no arranca y explica por qué.
5. **Cuando la MFA de un rol caduca** (5.4.3), el front recibe `403 auth.mfa_required` y lleva al usuario a un **login completo** (`GET /auth/login?return_to=…`): sesión nueva y la anterior revocada (5.1.8). No hay `GET /auth/stepup` ni rotación del token en la misma fila, y `app_runtime` no tiene `UPDATE` sobre `mfa_auth_time` ni `token_hash` (7.1). Un proveedor futuro que permita el step-up lo concedería en una migración nueva.

### 5.3 Transacciones: tres formas, y ninguna más

| Función | Fija | Para qué |
| --- | --- | --- |
| `WithTenantTx(ctx, fn)` | `app.tenant_id`, `app.user_id`, `app.scope` | Todo acceso a datos de un tenant. Requiere un `Principal` con tenant |
| `WithUserTx(ctx, fn)` | Solo `app.user_id` | Flujos del usuario sin tenant: middleware de tenant, `/me`, `POST /tenants`, aceptar invitación |
| `WithAnonTx(ctx, fn)` | Nada | Login, callback y eventos de auditoría sin usuario |

Las tres viven en `platform/db`. Las dos últimas solo pueden llamar a funciones `SECURITY DEFINER` o tocar tablas de identidad, nunca tablas de negocio: sin `app.tenant_id`, RLS hace fallar cualquier intento.

No se anidan: dentro de una, llamar a otra falla (`ErrNestedTx`). La única excepción es `db.Independent(ctx)`: la siguiente transacción abre otra conexión del pool y hace commit pase lo que pase con la exterior. Es para escrituras que deben sobrevivir al rollback de quien llama; hoy, solo la auditoría de `authz.denied` (5.4.5). Fija `lock_timeout` (2 s): la transacción exterior sigue abierta y podría tener bloqueada una fila que necesite una clave foránea de la escritura (por ejemplo, `tenants … FOR UPDATE`); sin ese límite sería un interbloqueo que Postgres no detecta.

**El callback usa dos transacciones, y no son atómicas.**
- `WithAnonTx` para resolver el usuario (5.1.5 a 5.1.7): todavía no hay usuario; si hay rechazo, aquí solo se registra su evento.
- `WithUserTx`, con el usuario ya resuelto, para revocar la sesión anterior, crear la nueva, actualizar `last_login_at` y registrar `auth.login`. Va aparte para que `record_audit` tome el actor de `app.user_id`: en una transacción anónima, `auth.login` quedaría sin actor.

Se acepta que no sean atómicas porque la primera solo deja estados que un login posterior trata igual que si viniera de él: un usuario y una identidad nuevos, o un email sincronizado. Si la segunda falla, el login responde con error, no hay sesión ni cookie, y el siguiente intento encuentra la identidad (5.1.5) y sigue. Un usuario deshabilitado entre las dos transacciones obtiene una sesión que el middleware rechaza en la primera petición (5.4.1).

### 5.4 Cada petición autenticada

Qué exige cada ruta lo declara el contrato OpenAPI con `security`: por defecto, sesión y tenant (`session` y `tenant`); las rutas sin tenant, `[{session: []}]`; las públicas, `[]`. `routegen` lo convierte en `gen/routes` y `httpx.RouteSecurity` lo deja en el contexto de cada petición. Falla cerrado: una ruta registrada a mano que el contrato no declara exige sesión y tenant, y una que no existe, sesión.

1. **Sesión**: busca el hash con un join a `users`. `401` si está revocada o caducada (inactividad comparada con el `last_seen_at` **anterior**) o si el usuario está deshabilitado. Después actualiza `last_seen_at`, como mucho una vez cada 5 minutos.
2. **Tenant**: valida que `X-Tenant-ID` es un UUID y, con `WithUserTx`, llama a `auth_membership(tenant_id)`: rol, estado del tenant y del usuario. Si algo falla (cabecera ausente o mal formada, tenant inexistente o suspendido, sin membresía, usuario deshabilitado), el mismo `403 auth.tenant_forbidden`. **No se audita**: cualquiera con sesión podría llenar el registro con UUIDs inventados, y en el de otro tenant no puede entrar (sección 7.4, `TN031`). Se registra en el log a nivel `Warn`, con `user_id`, el motivo y el tenant pedido solo si es un UUID válido.
3. **MFA del rol**: si el rol la requiere y `mfa_auth_time` es nulo o más antiguo que la antigüedad máxima del rol: `403 auth.mfa_required`.
4. **Principal** en el contexto (`platform/authctx`): `UserID`, `TenantID`, `Role`, `Permissions`, `Scope`. `user_id` y `tenant_id`, a los logs, también al registro de acceso.
5. **Caso de uso**: `Authorizer.Require(ctx, perm)` (`platform/authz`), en cualquier punto del caso de uso, también dentro de su transacción. Sin el permiso: `403 authz.denied` y el evento `authz.denied` en la auditoría del tenant, escrito en una transacción propia (`db.Independent`, 5.3) que sobrevive al rollback de quien llama. Si no se puede auditar, deniega igualmente y lo registra en el log como `Error`.
6. **Datos**: `WithTenantTx`.

El rol del middleware sirve para autorizar la petición. Las operaciones que dependen del rol del actor para modificar otros roles **lo releen y bloquean dentro de su transacción** (sección 5.7).

Todas las respuestas llevan `Cache-Control: no-store`.

### 5.5 Logout, revocación y tenant

- `POST /auth/logout`: revoca la sesión, borra la cookie y cierra la sesión de AuthKit. `POST /auth/logout-all`: revoca todas las del usuario.
- **Deshabilitar a un usuario** (comando de administración): en una transacción, revoca sus sesiones y **sus invitaciones pendientes**. Si es el último owner de algún tenant, exige confirmación explícita.
- **Crear un tenant**: `POST /tenants` (sesión, sin tenant, si `TENANT_SIGNUP_ENABLED`) llama a `create_tenant_with_owner(name, slug, ip_prefix)`, que registra `tenant.created` y `membership.created` en la misma transacción. Slug ocupado: `409 tenant.slug_taken` (revela que existe, y se acepta: un slug no da acceso a nada).

### 5.6 Invitaciones

1. **Crear**: exige `members:invite`, que el rol ofrecido **exista en el catálogo** de la app y que esté incluido en el del actor. El rol `owner` solo lo puede ofrecer un owner (sección 8).
2. **Duplicados**: ya es miembro → `409 invitation.already_member`. Pendiente sin caducar → `409 invitation.pending`, con opción de reenviar (rota el token y renueva la caducidad). Pendiente caducada → se revoca y se crea la nueva.
3. **Token**: un solo uso (se guarda su hash), 7 días, en el fragmento del enlace (`/invite#token=…`), con `Referrer-Policy: no-referrer` en la página. Antes del login, el front lo guarda en `sessionStorage`, borra el fragmento y lo recupera al volver. Nunca va en `return_to`.
4. **Revocación automática**: cuando quien invitó es degradado a un rol que ya no incluye el ofrecido o pierde `members:invite`, es expulsado, abandona el tenant o es deshabilitado, **sus invitaciones pendientes se revocan en la misma transacción**. Así una invitación pendiente siempre está respaldada por alguien que aún podría emitirla, sin revalidarlo al aceptar. Se registra `invitation.revoked` con el motivo.
5. **Aceptar**: el front llama a `POST /invitations/accept` con el token en el cuerpo (sesión, sin tenant). Con `WithUserTx`, una sola función, `accept_invitation(token_hash, ip_prefix)`, que revalida todo por sí misma:
   - rechaza a un usuario actual inexistente o deshabilitado;
   - bloquea la invitación con `FOR UPDATE`;
   - comprueba que no está usada, revocada ni caducada y que el tenant está activo;
   - compara su email con el email del usuario actual, leído de `users` a partir de `app.user_id`, y lo rechaza si ese email no está verificado;
   - comprueba que el usuario no es ya miembro;
   - crea la membresía y marca la invitación como aceptada.
6. La misma función registra `invitation.accepted` y `membership.created`: sin `app.tenant_id`, `record_audit` no admite eventos del tenant (`TN031`).

Caso límite aceptado: si un despliegue cambia los permisos de un rol, las invitaciones ya emitidas por ese rol no se revisan. Caducan en 7 días como máximo.

### 5.7 Gestión de miembros

`PATCH` y `DELETE /members/{membership_id}` exigen `members:manage`. Dentro de la transacción del caso de uso:

1. **Relee y bloquea** (`FOR UPDATE`) la membresía del actor; si ya no tiene `members:manage`: `403`. `DELETE /members/me` no usa este paso: con alcance `own`, `own_write` también se aplica a `SELECT … FOR UPDATE` e impide bloquear la propia fila, que no aparecería. Abandonar no depende del rol del actor, así que no necesita releerlo.
2. Bloquea las membresías de los owners del tenant.
3. Aplica las reglas:
   - **Sobre otros**: solo sobre miembros cuyo rol esté **estrictamente incluido** en el del actor. Un owner puede actuar sobre otros owners.
   - **Rol nuevo**: debe existir en el catálogo y estar incluido en el del actor. **`owner` solo lo concede un owner.**
   - **El propio rol** no se cambia. Se puede abandonar el tenant con `DELETE /members/me`.
   - **Al menos un owner por tenant**: si no, `409 membership.last_owner`.
4. Si el cambio quita permisos de invitación al afectado, revoca sus invitaciones pendientes (5.6.4).
5. Al quitar a un miembro, se publica el evento de dominio `identity.member_removed` en la misma transacción (outbox). Cada app que tenga tablas de acceso **debe** manejarlo y borrar las filas de acceso de ese usuario en ese tenant, para que no las recupere si vuelve a ser invitado. La plantilla trae un test de ejemplo que cada app replica para sus tablas de acceso.

### 5.8 Borrado de un usuario (RGPD)

Comando de administración, con `app_admin`:
1. Si es el último owner de algún tenant, se detiene hasta transferir la propiedad (el borrado de un tenant queda fuera de esta versión, sección 12).
2. Revoca sesiones y borra identidades, invitaciones pendientes que emitió y membresías (publicando `identity.member_removed` por cada una). `memberships.user_id` es `ON DELETE RESTRICT`: si queda alguna membresía, el borrado del usuario falla en lugar de quitarla sin evento.
3. Borra la fila de `users`; `audit_log.actor_user_id` y el `invited_by` de sus invitaciones ya aceptadas, revocadas o caducadas pasan a `NULL`.
4. Cada app documenta qué hace con sus filas de negocio de ese usuario (anonimizar o conservar por obligación legal).

## 6. Sesiones y seguridad del navegador

- **Cookie** `__Host-session`: `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`, sin `Domain`.
- **Caducidad**: por inactividad y absoluta, configurables (7 y 30 días por defecto).
- **MFA por rol**: cada rol declara si la requiere y su antigüedad máxima (por ejemplo, 12 horas para `owner` y `lawyer`). Con WorkOS exige `WORKOS_MFA_REQUIRED=true` (5.2); al caducar, login completo.
- **CSRF**: `SameSite=Lax` y `Origin` en `ALLOWED_ORIGINS` en los métodos que modifican datos.
- **CORS**: `ALLOWED_ORIGINS` explícita, con `credentials`, sin comodines.
- **Límite de peticiones**, `429 rate_limit.exceeded`:
  - por IP en `/auth/login` y `/auth/callback` (sin sesión todavía): la dirección en IPv4 (/32) y su /64 en IPv6, para que rotar direcciones dentro de un bloque no renueve la cuota. La IP sale de `TRUSTED_CLIENT_IP_HEADER` o de `RemoteAddr`, nunca de `X-Forwarded-For`;
  - por usuario en las rutas autenticadas que lo necesitan: `POST /invitations`, `POST /invitations/{id}/resend`, `POST /invitations/accept` y `POST /tenants` (paso 5);
  - detrás del puerto `ratelimit.Limiter`, con implementación en memoria: **cada réplica cuenta por separado**, así que el límite efectivo se multiplica por el número de réplicas. Un almacén compartido implementaría el mismo puerto.
- **Datos personales**: emails con `pii.Email`; IPs solo truncadas: `/24` en IPv4 y `/48` en IPv6, guardadas como `cidr` (`sessions.ip_prefix`, `audit_log.ip_prefix`). Go trunca antes de escribir; un `CHECK` en cada columna rechaza una IP completa (`/32`, `/128`) o cualquier otra máscara, y el tipo `cidr` rechaza bits de host tras la máscara.

## 7. RLS

### 7.1 Roles de base de datos

- `app_owner`: dueño de las tablas; solo migraciones. Puede desactivar RLS como dueño y hacer `SET ROLE app_security` (`BYPASSRLS`), así que sus credenciales (`MIGRATIONS_DATABASE_URL`) equivalen a acceso total a los datos.
- `app_runtime`: la aplicación; sin `BYPASSRLS` y sin ser dueño de nada. Nunca un `UPDATE` sin columnas: cada tabla lista las que la aplicación cambia. La lista completa está fijada en `runtime_privileges.golden` (sección 11). Permisos de tabla:
  - `users`: `SELECT`, `INSERT` y `UPDATE (email, email_verified, last_login_at, updated_at)`; sin `DELETE` (solo `app_admin`, sección 5.8). `email` cambia en el login si cambió en el proveedor (5.1.7), y **en la misma sentencia** `email_verified` toma el valor del proveedor: nunca se conserva la verificación de otro email, porque `accept_invitation` se fía de ella (issue #4). `disabled_at` es de `app_admin`.
  - `user_identities`: `SELECT`, `INSERT`; ni `UPDATE` (una identidad vinculada es inmutable) ni `DELETE` (solo `app_admin`).
  - `sessions`: `SELECT`, `INSERT`, `DELETE` y `UPDATE (last_seen_at, idle_expires_at, revoked_at, updated_at)`: el toque de cada petición (5.4.1) y la revocación (5.1.8, 5.5). Sin step-up (5.2), `mfa_auth_time` y `token_hash` se fijan en el login y no cambian, igual que `auth_time`, `absolute_expires_at`, `provider_session_id` (la sesión de AuthKit, para cerrarla en el logout) y el usuario.
  - `tenants`: `SELECT` y `UPDATE (name, updated_at)`, filtrados por RLS; sin `INSERT` (se crean con `create_tenant_with_owner`) ni `DELETE` (sección 12). El estado (`status`) solo lo cambia `app_admin`.
  - `memberships`: `SELECT`, `DELETE` y `UPDATE (role, updated_at)`, filtrados por RLS; sin `INSERT` (solo `create_tenant_with_owner` y `accept_invitation`). Sin `UPDATE` de `user_id`: pasar una membresía a otro usuario lo admitiría sin invitación.
  - `invitations`: `SELECT`, `INSERT`, `DELETE` y `UPDATE (token_hash, expires_at, revoked_at, updated_at)`, filtrados por RLS: reenviar rota el token y la caducidad (5.6.2) y revocar fija `revoked_at` (5.6.4). `accepted_at` solo lo fija `accept_invitation`; `email`, `role` e `invited_by` se fijan al crearla.
  - `audit_log`: solo `SELECT`; escribe con `record_audit()`.
- `app_security`: `BYPASSRLS`, sin login; dueño de las funciones de la sección 7.4. Sus permisos de tabla, en la misma sección.
- `app_admin`: `BYPASSRLS`; solo comandos de administración, auditados. Lee y escribe en todas las tablas salvo `audit_log`, donde solo lee.

### 7.2 Variables

Las fijan las tres funciones de transacción con `set_config(…, true)`: `app.tenant_id`, `app.user_id` (usuario de la sesión o usuario de sistema; siempre un UUID válido cuando se fija) y `app.scope` (`tenant` u `own`, según el permiso `scope:tenant`).

Sin variables, cualquier consulta a una tabla con RLS falla y no devuelve filas. El fallo llega ya del planificador, que evalúa `current_setting` de la política al planificar, antes de leer ninguna fila. Típicamente es `42704` (`undefined_object`) en una conexión nueva, porque la variable no existe, y `22P02` (`invalid_text_representation`) en una reutilizada, porque vuelve a `''` y falla el cast a `uuid`. El test acepta cualquiera de los dos códigos y exige que no haya filas. **Prohibido en las políticas** `current_setting(…, true)` y `NULLIF`. Las funciones `SECURITY DEFINER` sí pueden usar `current_setting('app.user_id', true)` para detectar que no hay usuario (por ejemplo, `record_audit` en eventos anónimos).

### 7.3 Políticas y alcance

**Política estándar**, permisiva y `FOR ALL`, en toda tabla con `tenant_id` (en `tenants`, sobre `id`):

```sql
CREATE POLICY tenant_isolation ON <tabla> FOR ALL
  USING (tenant_id = current_setting('app.tenant_id')::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);
```

**Alcance declarado**, uno de dos:
- **Compartida en el tenant**: `COMMENT ON TABLE <tabla> IS 'scope: tenant-shared'`.
- **Alcance propio**: una política `own_scope`, **`AS RESTRICTIVE` y `FOR ALL`**. Si fuera permisiva, Postgres la combinaría con OR con `tenant_isolation` y abriría las filas de **todos** los tenants.

```sql
-- Fila con dueño
CREATE POLICY own_scope ON <tabla> AS RESTRICTIVE FOR ALL
  USING (current_setting('app.scope') = 'tenant'
         OR owner_user_id = current_setting('app.user_id')::uuid);

-- Expediente con cliente y varias abogadas (tabla de acceso)
CREATE POLICY own_scope ON cases AS RESTRICTIVE FOR ALL
  USING (current_setting('app.scope') = 'tenant'
         OR EXISTS (SELECT 1 FROM case_access a
                    WHERE a.case_id = cases.id
                      AND a.user_id = current_setting('app.user_id')::uuid));

-- La propia tabla de acceso: cada uno ve solo sus filas de acceso
CREATE POLICY own_scope ON case_access AS RESTRICTIVE FOR ALL
  USING (current_setting('app.scope') = 'tenant'
         OR user_id = current_setting('app.user_id')::uuid);
```

Las tablas hijas (documentos de un expediente) declaran su alcance a través del padre con el mismo `EXISTS`.

**Tablas de acceso.** Una tabla que concede acceso a otras filas (`case_access`) se marca con `COMMENT ON TABLE <tabla> IS 'scope: access'`. Además de `own_scope`, lleva tres políticas restrictivas que solo permiten escribir con alcance `tenant`. Sin ellas, `own_scope` deja que un usuario con alcance `own` inserte una fila de acceso para sí mismo, o cambie el `case_id` de la suya, y llegue a cualquier expediente del tenant. Puede leer sus filas de acceso, pero nunca escribirlas:

```sql
COMMENT ON TABLE case_access IS 'scope: access';
CREATE POLICY own_write_insert ON case_access AS RESTRICTIVE FOR INSERT
  WITH CHECK (current_setting('app.scope') = 'tenant');
CREATE POLICY own_write_update ON case_access AS RESTRICTIVE FOR UPDATE
  USING (current_setting('app.scope') = 'tenant');
CREATE POLICY own_write_delete ON case_access AS RESTRICTIVE FOR DELETE
  USING (current_setting('app.scope') = 'tenant');
```

La política de `UPDATE` es obligatoria aunque `app_runtime` no tenga `UPDATE` en la tabla: un permiso concedido más adelante no debe abrir el alcance.

**Tablas hijas: claves foráneas compuestas, obligatorias.** Toda tabla que referencia a un padre con `tenant_id` (las tablas de acceso incluidas) lo hace con `(tenant_id, parent_id)` hacia un `UNIQUE (tenant_id, id)` del padre. RLS filtra las filas que se leen, pero una clave foránea simple a `cases(id)` no comprueba el tenant: una fila hija del tenant A podría colgar de un expediente de B. Con la clave compuesta, la base de datos lo rechaza (`23503`) sea cual sea el código:

```sql
CREATE TABLE cases (
  id        uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id uuid NOT NULL REFERENCES tenants (id) ON DELETE RESTRICT,
  …,
  UNIQUE (tenant_id, id)
);
CREATE TABLE case_documents (
  id        uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id uuid NOT NULL REFERENCES tenants (id) ON DELETE RESTRICT,
  case_id   uuid NOT NULL,
  …,
  FOREIGN KEY (tenant_id, case_id) REFERENCES cases (tenant_id, id)
);
CREATE INDEX case_documents_tenant_case_idx ON case_documents (tenant_id, case_id);
```

Los patrones completos, listos para copiar, están en `docs/patterns.md`.

**Políticas restrictivas adicionales.** Una tabla puede añadir más políticas `AS RESTRICTIVE` para estrechar una operación concreta; nunca políticas permisivas. `memberships` tiene una: con alcance `own`, el usuario puede leer y borrar su propia membresía (abandonar el tenant, sección 5.7), pero no modificarla.

```sql
CREATE POLICY own_write ON memberships AS RESTRICTIVE FOR UPDATE
  USING (current_setting('app.scope') = 'tenant');
```

Un `UPDATE` con alcance `own` no ve ninguna fila y no afecta a ninguna.

**El test de las migraciones**, leyendo `pg_class` y `pg_policies` tras migrar, comprueba en cada tabla con `tenant_id` y en `tenants` (con `id` como columna):
- `relrowsecurity` y `relforcerowsecurity` activos;
- `tenant_isolation` existe, es permisiva, `FOR ALL`, y su expresión compara la columna (`tenant_id`, o `id` en `tenants`) con `app.tenant_id`;
- o bien el comentario `scope: tenant-shared`, o bien `own_scope` restrictiva y `FOR ALL`;
- en las tablas con el comentario `scope: access`: `own_scope` y políticas restrictivas para `INSERT` (en `WITH CHECK`), `UPDATE` y `DELETE` (en `USING`) cuya expresión es exactamente `current_setting('app.scope') = 'tenant'`;
- toda clave foránea hacia otra tabla con `tenant_id` es compuesta y empareja `tenant_id` con `tenant_id` (las claves hacia `tenants` y `users` quedan exentas);
- ninguna política usa `current_setting(…, true)` ni `NULLIF`;
- ninguna política permisiva adicional además de `tenant_isolation` (las restrictivas adicionales se admiten, con las mismas comprobaciones de expresión).

### 7.4 Funciones `SECURITY DEFINER`

Todas: propiedad de `app_security`, `SET search_path = pg_catalog, extensions, pg_temp`, `REVOKE EXECUTE … FROM PUBLIC`, `GRANT EXECUTE` solo a `app_runtime`. El `GRANT EXECUTE` se hace **antes** de `ALTER FUNCTION … OWNER TO app_security`: `app_owner` es miembro de `app_security` sin `INHERIT`, así que tras el cambio de dueño ya no puede tocar los permisos de la función; el cambio de dueño conserva los ya concedidos. Leen el usuario actuante de `app.user_id`, nunca de un parámetro. **Cada una revalida por sí misma todo lo que protege.** Ninguna evalúa permisos.

| Función | Revalida | Devuelve o hace |
| --- | --- | --- |
| `auth_membership(tenant_id)` | — | Rol del usuario actual en el tenant, estado del tenant y del usuario |
| `auth_memberships_for_user()` | — | Tenants y roles del usuario actual (`/me`) |
| `create_tenant_with_owner(name, slug, ip_prefix)` | Usuario actual existe y no está deshabilitado | Tenant y membresía `owner` del usuario actual; registra `tenant.created` y `membership.created` |
| `accept_invitation(token_hash, ip_prefix)` | Todo lo de la sección 5.6.5, con `FOR UPDATE` | Membresía creada e invitación aceptada; registra `invitation.accepted` y `membership.created` |
| `tenant_status(tenant_id)` | — | Estado del tenant (trabajos de sistema) |
| `roles_in_use()` | — | Roles distintos en `memberships`, sin datos de tenants |
| `record_audit(…)` | Con `app.tenant_id` fijado, el tenant del evento es ese o nulo (`TN030`); sin él (o vacío), nulo (`TN031`) | Inserta en `audit_log`; actor nulo si no hay `app.user_id` |

Ya no existe ninguna función que devuelva datos de **otro** miembro.

**Auditoría sin tenant en la transacción.** Un evento solo entra en el registro de un tenant desde una transacción de ese tenant: en `WithUserTx` y `WithAnonTx`, o en una conexión reutilizada, `record_audit` solo acepta eventos sin tenant. Las dos funciones que crean membresías sin `app.tenant_id` escriben sus eventos ellas mismas, en la misma transacción. `ip_prefix` es siempre la IP del cliente ya truncada por quien llama, nunca la dirección completa.

**Permisos de tabla de `app_security`**, los justos para estas funciones y nada más:

| Tabla | Permisos | Para |
| --- | --- | --- |
| `users` | `SELECT` | Revalidar al usuario actual |
| `tenants` | `SELECT`, `INSERT` | Estado del tenant; `create_tenant_with_owner` |
| `memberships` | `SELECT`, `INSERT` | `auth_membership*`, `roles_in_use`; crear la membresía |
| `invitations` | `SELECT`, `UPDATE (accepted_at)` | `SELECT … FOR UPDATE` y marcarla aceptada |
| `audit_log` | `INSERT` | `record_audit`, `create_tenant_with_owner` y `accept_invitation` (sin `SELECT`: el id lo genera la función o la columna) |

**Códigos de error** (clase `TN`, definidos por la implementación; Go los traduce a `apperr`):

| Código | Significado |
| --- | --- |
| `TN001` | El usuario actual (`app.user_id`) no existe |
| `TN002` | El usuario actual está deshabilitado |
| `TN010` | Invitación no encontrada |
| `TN011` | Invitación ya aceptada |
| `TN012` | Invitación revocada |
| `TN013` | Invitación caducada |
| `TN014` | El tenant no está activo |
| `TN015` | El email de la invitación no coincide con el email verificado del usuario |
| `TN016` | El usuario ya es miembro del tenant |
| `TN020` | El slug del tenant ya existe |
| `TN030` | El tenant del evento de auditoría no coincide con `app.tenant_id` |
| `TN031` | El evento de auditoría tiene tenant, pero `app.tenant_id` no está fijado |

**Test de funciones**: todas tienen `search_path` fijo, `EXECUTE` revocado a `PUBLIC`, propietario `app_security` y ningún parámetro llamado `user_id`.

### 7.5 Dónde vive cada pieza

- `platform/authctx`: `Principal`, `Permission`, `Scope` y su lectura y escritura en el contexto.
- `platform/db`: `WithTenantTx`, `WithUserTx` y `WithAnonTx`.
- `modules/identity`: usuarios, identidades, sesiones, tenants, membresías, invitaciones y los middlewares de sesión y tenant.
- Defensa en profundidad: las consultas de sqlc incluyen `WHERE tenant_id = $1`.

### 7.6 Trabajos de River

- Los argumentos llevan `tenant_id` y `user_id`.
- **Trabajo de usuario**: el worker llama a `auth_membership(tenant_id)` con `WithUserTx`. Sin membresía, con el usuario deshabilitado o el tenant suspendido: se cancela sin reintentos y registra `job.cancelled` con el motivo, con `WithTenantTx` y el usuario de sistema: desde `WithUserTx` el evento no podría llevar el tenant (`TN031`). Si sigue, construye el `Principal` con los permisos actuales y usa `WithTenantTx`.
- **Trabajo de sistema**: no tiene membresía y no llama a `auth_membership`. El worker comprueba el tenant con `tenant_status(tenant_id)` y construye un `Principal` con el usuario de sistema, alcance `tenant` y un conjunto de permisos explícito definido en código. **No hay ningún caso especial para el usuario de sistema dentro de las funciones de la BD.** Se audita.

## 8. Permisos

- **Formato** `recurso:accion`, más `scope:tenant`.
- **Permisos de plataforma**: `members:read`, `members:invite`, `members:manage`, `audit:read`, y `tenant:own`, **reservado al rol `owner`**.
- **Roles** definidos en código por cada app. La plantilla trae:
  - `owner`: reservado, todos los permisos, incluido `tenant:own`;
  - `admin` y `member`.
- **Al arrancar**, la app valida su catálogo (`cmd/api/roles.go`, con `platform/authz`) y no arranca si falla: ningún rol salvo `owner` puede tener `tenant:own`, `owner` no se puede renombrar ni eliminar y tiene todos los permisos de plataforma y de los demás roles. Avisa, con un error en el log por cada uno, de los roles de `roles_in_use()` que no existen en el código.
- Gracias a `tenant:own`, **ningún rol puede incluir a `owner`**: la regla de inclusión ya impide que un admin conceda owner. Además, la regla "solo un owner concede owner" se comprueba explícitamente.
- **Cada rol declara** si requiere MFA y su antigüedad máxima.
- **Inclusión**: los permisos de A son un subconjunto de los de B. "Estrictamente incluido": subconjunto propio.
- **Rol desconocido**: cero permisos y error en el log. **Nunca se puede asignar**: invitar y cambiar de rol validan contra el catálogo.
- **Comprobación en el caso de uso.**

## 9. Auditoría

`auth.login`, `auth.logout`, `auth.login_failed`, `auth.email_in_use`, `session.revoked`, `user.disabled`, `user.deleted`, `tenant.created`, `membership.created`, `membership.role_changed`, `membership.removed`, `membership.left`, `invitation.created`, `invitation.resent`, `invitation.accepted`, `invitation.revoked`, `authz.denied`, `job.cancelled`.

El email del invitado no va en `metadata`. Retención configurable.

## 10. API

**URL base.** El backend no lleva prefijo `/api`: sus rutas son las de la tabla. El cliente generado (orval) antepone una URL base configurable, `VITE_API_BASE_URL`: `/api` en desarrollo, donde el proxy de Vite quita el prefijo y reenvía a la API, y `https://api.chaskicode.com` en producción, con `credentials: "include"` (mismo sitio que el front, CORS de la sección 6). `/auth/*` también se sirve por el proxy **sin prefijo**, porque AuthKit vuelve a `WORKOS_REDIRECT_URI` (`http://localhost:5173/auth/callback` en desarrollo) y la cookie debe quedar en el origen del front.

| Método y ruta | Sesión | Tenant | Permiso | Descripción |
| --- | --- | --- | --- | --- |
| `GET /auth/login` | No | No | — | Login |
| `GET /auth/callback` | No | No | — | Retorno desde AuthKit (el propósito lo dice el intento) |
| `POST /auth/logout` | Sí | No | — | Cierra la sesión |
| `POST /auth/logout-all` | Sí | No | — | Cierra todas las sesiones |
| `GET /me` | Sí | No | — | Usuario y membresías |
| `GET /tenant` | Sí | Sí | — | Tenant actual, con el rol, el alcance y los permisos del usuario |
| `POST /tenants` | Sí | No | — | Crea un tenant (si está habilitado) |
| `POST /invitations/accept` | Sí | No | — | Acepta una invitación |
| `GET /members` | Sí | Sí | `members:read` | Miembros del tenant |
| `DELETE /members/me` | Sí | Sí | — | Abandonar el tenant |
| `PATCH /members/{membership_id}` | Sí | Sí | `members:manage` | Cambiar el rol |
| `DELETE /members/{membership_id}` | Sí | Sí | `members:manage` | Quitar a un miembro |
| `GET /invitations` | Sí | Sí | `members:invite` | Invitaciones pendientes |
| `POST /invitations` | Sí | Sí | `members:invite` | Invitar |
| `POST /invitations/{id}/resend` | Sí | Sí | `members:invite` | Reenviar y rotar token |
| `DELETE /invitations/{id}` | Sí | Sí | `members:invite` | Revocar |
| `GET /audit` | Sí | Sí | `audit:read` | Auditoría del tenant |

Los `{id}` son de la membresía o de la invitación, nunca del usuario. El rol `client` de la app legal no tiene `members:read`, `members:invite`, `members:manage` ni `audit:read`.

## 11. Pruebas obligatorias

**Estructura de la BD** (test de migraciones y de funciones, sección 7.3 y 7.4)
- Cada tabla con `tenant_id`, y `tenants` por su `id`: RLS activo y forzado, `tenant_isolation` permisiva y `FOR ALL`, alcance declarado; si es `own_scope`, restrictiva y `FOR ALL`; ninguna otra política permisiva.
- Una tabla de prueba con `own_scope` permisiva hace fallar el test (el test se prueba a sí mismo).
- Una tabla de acceso sin la política de escritura de `INSERT`, `UPDATE` o `DELETE` hace fallar el test.
- Una tabla hija con una clave foránea simple hacia una tabla con `tenant_id` hace fallar el test.
- Funciones: `search_path`, `EXECUTE` revocado a `PUBLIC`, propietario y sin parámetro `user_id`.
- Foto de permisos: todos los de `app_runtime` (de tabla, de columna y de ejecución, también los heredados de `PUBLIC`) coinciden con `testdata/runtime_privileges.golden`. Cambiar ese fichero es una decisión de seguridad que se justifica en el PR.

**Aislamiento**
- Para cada endpoint con tenant: un usuario de A recibe `403` con el tenant B y no ve ni modifica datos de B forzando IDs.
- Sin variables, cualquier consulta a una tabla con RLS falla, en conexión nueva y reutilizada.
- Dos transacciones seguidas en la misma conexión no comparten variables.
- Alcance `own`: un cliente no ve expedientes, documentos, filas de acceso, membresías ni invitaciones de otros; una abogada sí.
- `GET /members` con rol `client`: `403`.

**Sesión y login**
- Sesión caducada (también por inactividad), revocada o inexistente: `401`. Usuario deshabilitado con sesión viva: `401`.
- `X-Tenant-ID` mal formado, inexistente, sin membresía o tenant suspendido: el mismo `403`.
- MFA ausente o más antigua que la máxima del rol: `403 auth.mfa_required`.
- MFA (5.2): con `WORKOS_MFA_REQUIRED=true`, `mfa_auth_time = auth_time`; con `SSO` o sin la variable, `NULL`; métodos informados sin `mfa` → login rechazado. La API no arranca si un rol exige MFA y la variable es `false`.
- El login revoca la sesión anterior del navegador. El propósito no se puede forzar desde la URL.
- Dos logins en paralelo funcionan; con más de 3 intentos se descarta el más antiguo.
- `return_to` con `//evil.com`, `/\evil.com`, `/\t/evil.com` o `https://evil.com`: rechazado.
- Identidad nueva con un email ya usado: `409`, sin escribir nada.

**Autorización y gestión**
- Un `admin` no puede conceder `owner` (ni invitando ni con `PATCH`), cambiar su propio rol, ni actuar sobre otro `admin` o sobre un `owner`.
- Un rol con los mismos permisos que `admin` salvo `tenant:own` tampoco puede conceder `owner`.
- El catálogo no arranca si un rol distinto de `owner` tiene `tenant:own`.
- No se puede asignar un rol inexistente en el catálogo.
- Un owner degradado mientras su petición está en curso no puede completar una acción que requiera ser owner.
- El tenant nunca se queda sin owner por degradar, expulsar o abandonar, tampoco con peticiones simultáneas.

**Invitaciones**
- Email distinto, token usado, caducado o revocado, tenant suspendido o usuario ya miembro: rechazada.
- Degradar, expulsar o deshabilitar a quien invitó revoca sus invitaciones pendientes.
- Dos aceptaciones simultáneas del mismo token: solo una crea la membresía.

**Tablas de acceso**
- Quitar a un miembro borra sus filas de acceso; si se le vuelve a invitar, no recupera expedientes antiguos.

**Trabajos**
- Trabajo de usuario expulsado, deshabilitado o con el tenant suspendido: se cancela sin tocar datos.
- Trabajo de sistema: funciona con alcance `tenant` sin membresía; se cancela si el tenant está suspendido.

## 12. Fuera de esta versión

Vincular identidades desde la sesión, borrado de un tenant (será un comando de administración con `app_admin`; hasta entonces las claves hacia `tenants` son `RESTRICT` y `app_runtime` no tiene `DELETE`), locales, roles personalizados por tenant, SSO empresarial, API keys, suplantación para soporte (un login de suplantación del panel de WorkOS se rechaza, 5.1.4), subdominios y OpenFGA.

## 13. Verificado en la documentación de WorkOS (noviembre de 2026)

- **PKCE y canje desde el backend**: sí. `GET /user_management/authorize` con `code_challenge` (`S256`) y `state`; `POST /user_management/authenticate` (`grant_type=authorization_code`, `code`, `code_verifier`, API key como `client_secret`). SDK oficial `github.com/workos/workos-go/v7`. [authorize](https://workos.com/docs/reference/authkit/authentication/get-authorization-url), [code](https://workos.com/docs/reference/authkit/authentication/code).
- **Qué devuelve**: el usuario (con `email_verified`), `authentication_method` (`Password`, `Passkey`, `MagicAuth`, `SSO`, OAuth…) y un access token con `sub`, `sid`, `org_id`, `role`, `permissions`, `exp`, `iat` y `auth_time` (última autenticación activa; no avanza al refrescar). **No hay `amr` ni ningún dato de MFA.** [sessions](https://workos.com/docs/authkit/sessions), [reauthentication](https://workos.com/docs/authkit/reauthentication).
- **Forzar reautenticación**: `max_age=0` (solo con `provider=authkit`). AuthKit elige el método (contraseña, MFA o el IdP de SSO); la app **no puede exigir MFA** ni sabe cuál se usó. Por eso no hay step-up (5.2).
- **MFA**: se puede exigir a todos los usuarios del entorno en el panel; **no se aplica a usuarios SSO**. No se documenta si cubre todos los métodos (passkey, magic auth): la comprobación manual del runbook incluye probarlo. [mfa](https://workos.com/docs/authkit/mfa).
- **Logout de AuthKit**: `GET /user_management/sessions/logout?session_id=<sid>`; vuelve al *Sign-out URI* configurado en el panel. [logout](https://workos.com/docs/reference/authkit/logout/get-logout-url).
- Pendiente: si `email_verified` puede ser cierto con emails no controlados por el usuario, y residencia de datos y DPA.

## 14. Plan de implementación

1. **Esquema y RLS**: esquema `extensions`, roles de BD, tablas, políticas, funciones `SECURITY DEFINER`, usuario de sistema y tests de estructura de la BD.
2. **Transacciones y contexto**: `platform/authctx`, `WithTenantTx`, `WithUserTx`, `WithAnonTx`, y tests de aislamiento, pool y alcance `own` con tablas de ejemplo (incluida una tabla de acceso).
3. **Login y sesiones**: adaptador de WorkOS, intentos, `/auth/*`, MFA deducida de la configuración (5.2), CSRF, CORS, IP de confianza y límite de peticiones.
4. **Principal y permisos**: middlewares de sesión y tenant, MFA por rol, `authz`, catálogo de roles con su validación al arrancar, y `/me`.
5. **Tenants, invitaciones, miembros y auditoría**, con la revocación automática y el evento `identity.member_removed`.
6. **Trabajos**: workers de usuario y de sistema.
7. **Administración**: deshabilitar y borrar usuario.
8. **Front**: login (también al caducar la MFA de un rol), selector de tenant, `X-Tenant-ID`, invitaciones (con `sessionStorage`) y miembros.