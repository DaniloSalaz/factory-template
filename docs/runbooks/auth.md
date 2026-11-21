# Runbook: login con WorkOS AuthKit

Diseño: `docs/designs/auth-y-tenancy.md` (secciones 5.1, 5.2, 5.5, 5.7, 6 y 13),
`docs/adr/0002-mfa-opcional-por-rol.md` y `docs/adr/0003-invariante-owner.md`.

## Variables

| Variable | Dónde | Notas |
|---|---|---|
| `WORKOS_CLIENT_ID` | `[env]` de `mise.toml` (o el entorno) | Empieza por `client_` |
| `WORKOS_API_KEY` | `secrets.env`, cifrado con SOPS + age | Nunca en `mise.toml`, `.env` ni logs (`pii.Secret`) |
| `WORKOS_REDIRECT_URI` | `[env]` | Registrada como Redirect URI en el panel; `https` fuera de desarrollo |
| `FRONTEND_ORIGIN` | `[env]` | Origen al que debe resolver `return_to` |
| `ALLOWED_ORIGINS` | `[env]` | Lista cerrada para CORS y CSRF; incluye `FRONTEND_ORIGIN`; sin comodines |
| `TRUSTED_CLIENT_IP_HEADER` | `[env]` | Vacía en desarrollo; `CF-Connecting-IP` por defecto en staging y producción; nunca `X-Forwarded-For` |
| `SESSION_IDLE_TTL` / `SESSION_ABSOLUTE_TTL` | `[env]` | 168h y 720h por defecto |
| `FRONT_BASE_URL` | `[env]` | Base de los enlaces de invitación (`/invite#token=…`); por defecto `FRONTEND_ORIGIN` |
| `TENANT_MAX_PER_USER` | `[env]` | Tenants que puede crear cada usuario (`POST /tenants`), también los que ya dejó; 5 por defecto, entre 1 y 1000 |

### Secretos con SOPS

mise no descifra ficheros dotenv cifrados con SOPS: los cargaría con el texto cifrado
(`ENC[…]`) como valor. Por eso `mise run dev` ejecuta la API con
`sops exec-env ../secrets.env 'go run .'`, y `secrets.env` **no** se declara en
`_.file`.

```sh
export SOPS_AGE_KEY_FILE=~/.config/sops/age/keys.txt   # en macOS sops no mira ahí por defecto
sops edit secrets.env                                   # WORKOS_API_KEY=sk_test_...
```

## Panel de WorkOS, por entorno

1. **Redirects → Redirect URIs**: `WORKOS_REDIRECT_URI` del entorno
   (desarrollo: `http://localhost:5173/auth/callback`).
2. **Redirects → Sign-out URIs**: el origen del front (desarrollo: `http://localhost:5173/`),
   como URI por defecto. El logout devuelve allí tras cerrar la sesión de AuthKit.
3. **Authentication → Features → Multi-factor auth**: **no** en `Required` (ADR 0002). La
   MFA la verifica la app después del login, y solo la exigen los roles del catálogo que
   lo dicen. Con `Required`, AuthKit pediría un factor a todos en cada login, además del
   que pide la app.

### Pasos exactos para pasar de `Required` a opcional

**Solo cuando el commit de la MFA opcional esté desplegado en ese entorno**, nunca antes:
el código anterior trataba como MFA cualquier login con contraseña mientras
`WORKOS_MFA_REQUIRED=true`.

1. Despliega la versión con la MFA opcional (en local: `git pull` de `main` y reinicia
   `mise run dev`). Comprueba que arranca y que `/healthz` responde 200.
2. Panel de WorkOS de **staging**, que es también el de desarrollo local (el
   `WORKOS_CLIENT_ID` de `mise.toml` es de staging): *Authentication → Features →
   Multi-factor auth* → elige la opción que no es `Required` (desactivado u opcional) →
   *Save changes*.
3. Si tienes `WORKOS_MFA_REQUIRED` en tu `.env` local, bórrala: la variable ya no existe y
   la configuración la ignoraría.
4. Comprueba el flujo con un usuario de prueba, como en "Prueba manual: MFA de un rol"
   (más abajo): entra (sin factor, AuthKit no debe pedir código), `GET /tenant` con un
   tenant donde es `owner` da `403 auth.mfa_enrollment_required`, da de alta el factor,
   verifica y `GET /tenant` da 200. Después, su paso 7: si AuthKit pide el código a un
   usuario que ya tiene factor.
5. Producción: los mismos pasos 1 a 4, en su panel, cuando exista.

## MFA: cómo funciona

WorkOS no informa de si hubo MFA en un login: el access token no trae `amr` ni nada
equivalente (diseño, sección 13). Por eso la app no deduce nada del login y verifica el
segundo factor ella misma (diseño, 5.2):

- un login **nunca** deja MFA en la sesión: `mfa_auth_time` nace `NULL`;
- `POST /me/mfa/totp` da de alta un factor TOTP en WorkOS; `POST /me/mfa/challenges`
  crea un challenge (un solo uso, 5 minutos, ligado a la sesión); `POST
  /me/mfa/totp/verify` envía el código a WorkOS;
- solo con `valid: true`, `mark_session_mfa` sustituye la sesión por una nueva (token
  nuevo, `mfa_auth_time = now()`) y revoca la anterior;
- un código ya aceptado con ese factor en los últimos 10 minutos se rechaza
  (`409 mfa.code_reused`); como mucho 5 intentos de verificación por usuario y por
  sesión en 15 minutos (`429 mfa.rate_limited`, por réplica);
- `GET /tenant` y las rutas con tenant exigen, si el rol lo pide, una MFA de la sesión
  dentro de su ventana (owner: 12 h): `403 auth.mfa_required` si el usuario tiene factor
  verificado, `403 auth.mfa_enrollment_required` si aún no.

Para ver qué pasa en la BD (como `app_admin`):

```sh
docker compose exec postgres psql -U app_admin -d factory -c \
  "SELECT f.id, f.verified_at, f.last_code_at, u.email FROM mfa_factors f JOIN users u ON u.id = f.user_id;"
docker compose exec postgres psql -U app_admin -d factory -c \
  "SELECT action, actor_user_id, target_id, metadata, created_at FROM audit_log
    WHERE action LIKE 'mfa.%' ORDER BY created_at DESC LIMIT 10;"
```

## Recuperación: un usuario ha perdido su dispositivo

Hasta que exista el comando de administración (paso 7, issue #18), a mano y
en este orden. Exige verificar antes la identidad del usuario por un canal ajeno a la
cuenta, y un ticket que lo justifique.

1. Localiza sus factores, como `app_admin`:

   ```sh
   docker compose exec -T postgres psql -U app_admin -d factory -v email='usuario@ejemplo.com' <<'SQL'
   SELECT f.id, f.provider_factor_id, f.verified_at
   FROM mfa_factors f JOIN users u ON u.id = f.user_id WHERE u.email = :'email';
   SQL
   ```

2. Bórralo en WorkOS. `provider_factor_id` es el `auth_factor_…` del paso 1:

   ```sh
   sops exec-env secrets.env 'curl -sS -X DELETE https://api.workos.com/auth/factors/auth_factor_XXXX \
     -H "Authorization: Bearer $WORKOS_API_KEY"' ; echo
   ```

   Un `404` significa que ya no estaba; sigue igualmente.

3. Bórralo en la BD, como `app_admin` y con el motivo. El disparador
   `audit_mfa_factor_removed` registra `mfa.factor_removed`, con `by = app_admin` y
   `reason` en `metadata`:

   ```sh
   docker compose exec -T postgres psql -U app_admin -d factory -v ON_ERROR_STOP=1 \
     -v factor='<id del paso 1>' -v reason='dispositivo perdido, ticket #123' <<'SQL'
   BEGIN;
   SELECT set_config('app.audit_reason', :'reason', true);
   DELETE FROM mfa_factors WHERE id = :'factor';
   COMMIT;
   SQL
   ```

4. Opcional, si se sospecha que alguien más tiene acceso a la cuenta: revoca sus
   sesiones (`UPDATE sessions SET revoked_at = now() WHERE user_id = … AND revoked_at IS NULL`,
   como `app_admin`).
5. Comprueba la auditoría:
   `SELECT metadata FROM audit_log WHERE action = 'mfa.factor_removed' AND target_id = '<id>';`
6. El usuario vuelve a entrar y da de alta un factor nuevo. Si su rol exige MFA, hasta
   entonces recibe `403 auth.mfa_enrollment_required` en ese tenant.

## Prueba manual en desarrollo

Requisitos: Docker, la clave age y el panel de staging configurado como arriba. Usa
Chrome o Firefox: aceptan cookies `Secure` (y el prefijo `__Host-`) en
`http://localhost`; Safari no.

```sh
mise run up && mise run migrate
mise run dev                      # terminal 1: API en :8080
cd web && npm run dev             # terminal 2: front en :5173
```

1. Abre `http://localhost:5173`: sin sesión, el front lleva a `/login`. Pulsa **Entrar**.
   AuthKit pide credenciales (siempre: `max_age=0`), sin código de MFA, y vuelve a
   `http://localhost:5173/`, que lleva al selector de organizaciones.
2. DevTools → Application → Cookies → `http://localhost:5173`: existe `__Host-session`
   (`HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`) y ya no queda ninguna `__Host-oauth_*`.
3. La sesión está en Postgres, solo con el hash:

   ```sh
   docker compose exec postgres psql -U app_admin -d factory -c \
     "SELECT id, encode(token_hash, 'hex') AS token_hash, auth_time, mfa_auth_time,
             ip_prefix, provider_session_id IS NOT NULL AS has_sid, revoked_at
        FROM sessions ORDER BY created_at DESC LIMIT 3;"
   ```

   `token_hash` tiene 64 caracteres hex y **no** es el valor de la cookie; el SHA-256 del
   valor de la cookie sí coincide:
   `printf '%s' '<valor de la cookie>' | shasum -a 256`.
4. La auditoría registra el login, como `app_admin`. Los eventos sin tenant (`auth.*`) solo
   los lee `app_admin` (diseño, 7.1). No uses `app_owner`: es el dueño de las tablas y
   `FORCE ROW LEVEL SECURITY` también le aplica, así que sin `app.tenant_id` falla con
   `unrecognized configuration parameter "app.tenant_id"`. Es lo esperado: RLS falla cerrado.

   ```sh
   docker compose exec postgres psql -U app_admin -d factory -c \
     "SELECT action, actor_user_id, ip_prefix FROM audit_log
       WHERE tenant_id IS NULL ORDER BY created_at DESC LIMIT 3;"
   ```
5. Pulsa **Salir**: la API revoca la sesión (`revoked_at` deja de ser `NULL`), borra la
   cookie y el navegador pasa por el logout de AuthKit antes de volver al front.
6. Pulsa **Entrar** de nuevo: AuthKit vuelve a pedir credenciales.

## Prueba manual: el front con WorkOS (paso 8a)

Diseño: 5.1, 5.2, 5.5 y 10.1; front: `web/README.md`. Con WorkOS de verdad y una app de
autenticación. Mismos requisitos que "Prueba manual en desarrollo" (Chrome o Firefox, el
panel de staging sin MFA `Required`). Usa un usuario **sin factor de MFA** en WorkOS para
ver el alta; si ya tiene uno, bórralo antes como en "Recuperación".

```sh
mise run up && mise run migrate
mise run dev                      # terminal 1: API en :8080
cd web && npm run dev             # terminal 2: front en :5173
psql_su()  { docker compose exec -T postgres psql -U factory -d factory -v ON_ERROR_STOP=1 -At "$@"; }
```

1. **Login**: abre `http://localhost:5173/tenants`. Sin sesión, el front lleva a
   `/login?return_to=%2Ftenants`. Pulsa **Entrar**; tras AuthKit vuelves a `/tenants` (el
   `return_to`), con tu email arriba a la derecha.
2. **Crear tenant**: en "Crear una organización", prueba el identificador `Con Mayúsculas`
   (error de formato bajo el campo) y `admin` (reservado). Después uno libre, p. ej.
   `prueba-front`. Al crearla, el front entra en `/t/<id>` y, como eres `owner` y el rol
   exige MFA, `GET /tenant` responde `403 auth.mfa_enrollment_required`: el front te lleva a
   **Dar de alta la verificación en dos pasos**.
3. **Alta de MFA como owner**: pulsa **Empezar el alta**, escanea el QR (o escribe la clave)
   y escribe el código. Vuelves a `/t/<id>` con el nombre, tu rol (`owner`) y los permisos.
   - DevTools → Application → Cookies: el valor de `__Host-session` ha cambiado (la sesión
     se sustituye al verificar).
   - DevTools → Application → Local Storage: solo `platform.lastTenantId`; ni la clave ni el
     URI de MFA están en ningún almacenamiento ni en la consola.
   - Recarga la página: el QR y la clave ya no se muestran en ningún sitio.
   - **Seguridad** (`/account/mfa`) muestra el factor como verificado y la hora de la
     verificación de esta sesión.
4. **Verificación al caducar la ventana** (owner: 12 h). En vez de esperar, envejece la MFA
   de tu sesión como superusuario, solo en local:

   ```sh
   psql_su -v email='<tu email>' <<'SQL'
   UPDATE sessions SET mfa_auth_time = now() - interval '13 hours'
   WHERE user_id = (SELECT id FROM users WHERE email = :'email') AND revoked_at IS NULL;
   SQL
   ```

   Recarga `/t/<id>`: `GET /tenant` responde `403 auth.mfa_required` y el front te lleva a
   **Introduce tu código** (`/account/mfa/verify?return_to=…`). Escribe un código mal: el
   error sale bajo el campo y el siguiente intento usa otro challenge. Con el bueno vuelves
   a `/t/<id>`.
5. **Volver desde `/`**: abre `http://localhost:5173/`: te lleva directamente a `/t/<id>`
   (el último tenant usado).
6. **Logout**: pulsa **Salir**. La API revoca la sesión y el navegador pasa por el logout de
   AuthKit antes de volver al front, que, sin sesión, te lleva a `/login`. Entra otra vez y
   prueba **Cerrar todas las sesiones**: con una segunda ventana privada con sesión abierta,
   su siguiente acción también acaba en `/login`.
7. Anota cualquier diferencia con lo esperado (paso, captura, consola) en el PR o en una
   issue. Limpieza: borra el factor como en "Recuperación" y el tenant como en "Prueba
   manual: tenants y miembros" (paso 10).

## Prueba manual: miembros e invitaciones en el front con WorkOS (paso 8b)

Diseño: 5.6, 5.7 y 10.1; front: `web/README.md`. Con WorkOS de verdad y **dos cuentas
tuyas** (A y B, emails distintos y verificados en AuthKit). A es owner, con MFA, de un
tenant: el de "Prueba manual: el front con WorkOS (paso 8a)" sirve. B usa otro navegador o
una ventana privada, para que las sesiones no se mezclen.

```sh
mise run up && mise run migrate
mise run dev 2>&1 | tee /tmp/factory-api.log     # terminal 1: el log trae el enlace
cd web && npm run dev                            # terminal 2
last_link() { grep '"invitation link (development only)"' /tmp/factory-api.log | tail -1 | sed -E 's/.*"link":"([^"]*)".*/\1/'; }
```

1. **Invitar** (A): en `/t/<id>` la navegación muestra **Miembros** e **Invitaciones**. En
   **Invitaciones**, el desplegable de rol ofrece `owner`, `admin` y `member` (los
   `can_offer` de `GET /tenant/roles`). Invita al email de B como `admin`: sale "Invitación
   enviada" y la invitación aparece en la lista con su caducidad (7 días). Invitar otra vez
   el mismo email da "ya tiene una invitación pendiente" y el botón **Reenviar la
   pendiente**: púlsalo (rota el token, así que usa el enlace nuevo del log).
2. **Abrir el enlace sin sesión** (B, ventana privada): pega `last_link`. El front lleva a
   `/login?return_to=%2Finvite`. Comprueba:
   - la barra de direcciones y el historial (botón atrás, `chrome://history`) muestran
     `/invite`, nunca `#token=…`;
   - DevTools → Application → Session Storage: `platform.inviteToken` con el token; nada en
     Local Storage;
   - DevTools → Network: ninguna petición lleva el token en la URL ni en `return_to`, y
     ninguna lleva cabecera `Referer` (meta `no-referrer` de `index.html`).
3. **Entrar y aceptar** (B): pulsa **Entrar** y entra en AuthKit con la cuenta B. Vuelves a
   `/invite`, que llama a `POST /invitations/accept` y te lleva a `/t/<id>` con rol `admin`.
   Session Storage ya no tiene `platform.inviteToken`. Abrir el mismo enlace otra vez da
   "Esta invitación ya se ha aceptado".
4. **Cambiar rol** (A): en **Miembros**, cambia a B a `member` con **Cambiar rol**. B
   recarga: su rol es `member` e **Invitaciones** desaparece de su navegación (el cambio
   vale en la siguiente petición). Como admin, B no habría visto `owner` entre los roles.
5. **Expulsar** (A): **Expulsar** a B y confirma. La siguiente acción de B acaba en
   `/tenants` con el aviso "No tienes acceso a esa organización".
6. **Email distinto** (opcional): A invita a un tercer email; B, con su sesión, abre ese
   enlace. Sale "La invitación es para otro email…" y **Salir para entrar con otra cuenta**.
7. **Último owner** (A): en tu fila (**Tú**), **Salir de la organización** y confirma: sale
   "La organización se quedaría sin propietario" y sigues dentro.
8. **Renombrar** (A): en **Resumen**, cambia el nombre y **Guardar**: cambia el título y el
   nombre en `/tenants`.
9. **429**: la API manda `Retry-After` en todos los `429`. Para verlo sin esperar, invita y
   reenvía hasta pasar el límite (20 por hora): el aviso dice cuántos minutos esperar.
   Reinicia la API para vaciar el contador (está en memoria).
10. Anota cualquier diferencia en el PR. Limpieza: revoca las invitaciones pendientes desde
    la pantalla y borra el tenant como en "Prueba manual: tenants y miembros" (paso 10).

## Prueba manual: `/me`, tenant y `X-Tenant-ID` ajeno

Dos roles, cada uno para lo suyo:
- **Preparar datos** (tenants, usuarios, eventos de prueba): el superusuario local
  `factory`, que salta RLS. Esta prueba necesita un tenant ajeno y membresías ya hechas;
  para crear tenants por la API, "Prueba manual: tenants y miembros".
  **Nunca así en staging ni en producción.**
- **Comprobar**: la API, o `app_runtime` con las mismas variables que fija
  `db.WithTenantTx`. Una comprobación hecha como superusuario no prueba nada: se salta RLS.

Desde la raíz del repo, con `mise run up && mise run migrate` y la API en marcha
(`mise run dev`). Los bloques valen para bash y zsh.

```sh
# Funciones y no variables: zsh no parte en palabras una variable sin comillas.
psql_su()  { docker compose exec -T postgres psql -U factory -d factory -v ON_ERROR_STOP=1 -At "$@"; }
psql_app() { docker compose exec -T postgres psql -U app_runtime -d factory -v ON_ERROR_STOP=1 "$@"; }
```

1. **Sesión.** Elige una:
   - **A, con navegador**: entra en el front como arriba. Copia el valor de la cookie
     `__Host-session` en `SESSION` y tu email en `EMAIL`.
   - **B, sin navegador** (solo en local): crea un usuario y una sesión de una hora.
     En la BD solo va el hash del token, como en un login real:

     ```sh
     EMAIL=runbook@example.test
     SESSION=$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')
     psql_su -v email="$EMAIL" -v token="$SESSION" <<'SQL'
     INSERT INTO users (email, email_verified, display_name) VALUES (:'email', true, 'Runbook');
     INSERT INTO sessions (token_hash, user_id, auth_time, last_seen_at, idle_expires_at, absolute_expires_at)
     SELECT sha256(convert_to(:'token', 'UTF8')), id, now(), now(), now() + interval '1 hour', now() + interval '1 hour'
     FROM users WHERE email = :'email';
     SQL
     ```

2. **Preparación**, como `factory`:
   - el tenant `prueba`, donde tú eres `admin` (un rol sin MFA: aquí se prueba el
     aislamiento, no la MFA);
   - el tenant `ajeno`, con otra usuaria como `owner`;
   - un evento de auditoría en cada uno.

   ```sh
   psql_su -v email="$EMAIL" <<'SQL'
   INSERT INTO users (email, email_verified, display_name) VALUES ('otra.runbook@example.test', true, 'Otra');
   INSERT INTO tenants (name, slug) VALUES ('Prueba', 'prueba'), ('Ajeno', 'ajeno');
   INSERT INTO memberships (tenant_id, user_id, role)
     SELECT t.id, u.id, 'admin' FROM tenants t, users u WHERE t.slug = 'prueba' AND u.email = :'email'
     UNION ALL
     SELECT t.id, u.id, 'owner' FROM tenants t, users u WHERE t.slug = 'ajeno' AND u.email = 'otra.runbook@example.test';
   INSERT INTO audit_log (tenant_id, action, target_type, target_id)
     SELECT id, 'runbook.seed', 'tenant', slug FROM tenants WHERE slug IN ('prueba', 'ajeno');
   SQL
   MIO=$(psql_su <<< "SELECT id FROM tenants WHERE slug = 'prueba'")
   AJENO=$(psql_su <<< "SELECT id FROM tenants WHERE slug = 'ajeno'")
   USER_ID=$(psql_su -v email="$EMAIL" <<< "SELECT id FROM users WHERE email = :'email'")
   echo "MIO=$MIO AJENO=$AJENO USER_ID=$USER_ID"
   ```

3. **`GET /me`**: tu usuario y solo la membresía de `prueba`, sin `ajeno`:

   ```sh
   curl -s localhost:8080/me -H "Cookie: __Host-session=$SESSION"; echo
   ```

4. **`GET /tenant` con tu tenant**: `200`, con `role: admin`, `scope: tenant` y sus
   permisos, sin `tenant:own`:

   ```sh
   curl -s localhost:8080/tenant -H "Cookie: __Host-session=$SESSION" -H "X-Tenant-ID: $MIO"; echo
   ```

5. **Tenant ajeno, inventado o mal formado**: el mismo `403 auth.tenant_forbidden` (cuerpo
   idéntico salvo `request_id`). En el log de la API, un `Warn` "tenant refused" con
   `requested_tenant_id`, salvo en el mal formado:

   ```sh
   for t in "$AJENO" 0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f9999 no-es-un-uuid; do
     curl -s localhost:8080/tenant -H "Cookie: __Host-session=$SESSION" -H "X-Tenant-ID: $t" \
       | sed 's/,"request_id":"[^"]*"//'; echo
   done
   ```

6. **Aislamiento en la BD**, como `app_runtime` y con el contexto del tenant A (`prueba`).
   Es la misma sentencia que ejecuta `db.WithTenantTx` al abrir la transacción.
   - `de_a` es al menos 1 y `de_otros` es 0 en las dos tablas.
   - Pedir B explícitamente no devuelve nada.
   - En `audit_log` solo está `runbook.seed`: los 403 del paso 5 no se auditan.

   ```sh
   psql_app -v a="$MIO" -v b="$AJENO" -v u="$USER_ID" <<'SQL'
   BEGIN;
   SELECT set_config('app.tenant_id', :'a', true), set_config('app.user_id', :'u', true),
          set_config('app.scope', 'tenant', true) \g /dev/null
   SELECT 'memberships' AS tabla, count(*) FILTER (WHERE tenant_id = :'a') AS de_a,
          count(*) FILTER (WHERE tenant_id <> :'a') AS de_otros FROM memberships
   UNION ALL
   SELECT 'audit_log', count(*) FILTER (WHERE tenant_id = :'a'),
          count(*) FILTER (WHERE tenant_id IS DISTINCT FROM :'a') FROM audit_log;
   SELECT 'memberships de B' AS consulta, count(*) FROM memberships WHERE tenant_id = :'b'
   UNION ALL
   SELECT 'audit_log de B', count(*) FROM audit_log WHERE tenant_id = :'b';
   SELECT action, target_id FROM audit_log ORDER BY created_at;
   COMMIT;
   SQL
   ```

7. **Sin contexto, RLS falla cerrado**: como `app_runtime` y sin variables, la consulta da
   error en lugar de filas:

   ```sh
   psql_app <<< "SELECT count(*) FROM audit_log;"
   # ERROR:  unrecognized configuration parameter "app.tenant_id"
   ```

8. **Log de acceso**: en la salida de `mise run dev`, los registros `"msg":"http request"`
   de los pasos 3 a 5 llevan `user_id` y `pattern`; el del paso 4, también `tenant_id`.

9. **Limpieza**, como `factory`:

   ```sh
   psql_su -v email="$EMAIL" <<'SQL'
   DELETE FROM audit_log WHERE action = 'runbook.seed';
   DELETE FROM memberships WHERE tenant_id IN (SELECT id FROM tenants WHERE slug IN ('prueba', 'ajeno'));
   DELETE FROM tenants WHERE slug IN ('prueba', 'ajeno');
   DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE email IN (:'email', 'otra.runbook@example.test'))
     AND :'email' = 'runbook@example.test';
   DELETE FROM users WHERE email = 'otra.runbook@example.test'
      OR (email = :'email' AND :'email' = 'runbook@example.test');
   SQL
   ```

   Con la opción A no se borra tu usuario ni tus sesiones: solo los de prueba.

## Prueba manual: tenants y miembros

Diseño: 5.5 y 5.7, `docs/adr/0003-invariante-owner.md`. Sin navegador y solo en local:
las sesiones se crean como en la opción B de la sección anterior. Mismas reglas:
**preparar** con el superusuario `factory` (sesiones, y las membresías que llegarán con
las invitaciones del paso 5b); **comprobar** con la API o con `app_runtime`.

Desde la raíz del repo, con `mise run up && mise run migrate` y la API en marcha
(`mise run dev`). Los bloques valen para bash y zsh.

```sh
psql_su()  { docker compose exec -T postgres psql -U factory -d factory -v ON_ERROR_STOP=1 -At "$@"; }
psql_app() { docker compose exec -T postgres psql -U app_runtime -d factory -v ON_ERROR_STOP=1 "$@"; }
# api METHOD PATH SESSION [TENANT] [BODY]: cuerpo y estado HTTP, sin request_id.
api() {
  local args=(-s -w ' → %{http_code}\n' -X "$1" "localhost:8080$2" -H "Cookie: __Host-session=$3"
              -H "Origin: http://localhost:5173" -H "Content-Type: application/json")
  [ -n "$4" ] && args+=(-H "X-Tenant-ID: $4")
  [ -n "$5" ] && args+=(-d "$5")
  curl "${args[@]}" | sed 's/,"request_id":"[^"]*"//'
}
```

1. **Tres usuarios con sesión**: Ana creará el tenant; Bea y Carla serán miembros.

   ```sh
   new_session() {
     local token; token=$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')
     psql_su -v email="$1" -v token="$token" >/dev/null <<'SQL'
   INSERT INTO users (email, email_verified, display_name) VALUES (:'email', true, split_part(:'email', '@', 1));
   INSERT INTO sessions (token_hash, user_id, auth_time, last_seen_at, idle_expires_at, absolute_expires_at)
   SELECT sha256(convert_to(:'token', 'UTF8')), id, now(), now(), now() + interval '1 hour', now() + interval '1 hour'
   FROM users WHERE email = :'email';
   SQL
     printf '%s' "$token"
   }
   ANA=$(new_session ana.runbook@example.test)
   BEA=$(new_session bea.runbook@example.test)
   CARLA=$(new_session carla.runbook@example.test)
   ANA_ID=$(psql_su <<< "SELECT id FROM users WHERE email = 'ana.runbook@example.test'")
   BEA_ID=$(psql_su <<< "SELECT id FROM users WHERE email = 'bea.runbook@example.test'")
   CARLA_ID=$(psql_su <<< "SELECT id FROM users WHERE email = 'carla.runbook@example.test'")
   ```

2. **Crear el tenant** (solo sesión, sin `X-Tenant-ID`): un slug con mayúsculas y uno
   reservado dan `422` con códigos distintos; el bueno, `201` con Ana como `owner`. Repetir
   el slug da `409 tenant.slug_taken`.

   ```sh
   api POST /tenants "$ANA" "" '{"name":"Despacho","slug":"Despacho Ana"}'
   api POST /tenants "$ANA" "" '{"name":"Despacho","slug":"admin"}'
   api POST /tenants "$ANA" "" '{"name":"Despacho Ana","slug":"despacho-ana"}'
   api POST /tenants "$ANA" "" '{"name":"Otro","slug":"despacho-ana"}'
   T=$(psql_su <<< "SELECT id FROM tenants WHERE slug = 'despacho-ana'")
   ```

   Pasar de `TENANT_MAX_PER_USER` (5 por defecto) tenants creados da
   `403 tenant.limit_reached`.

3. **La MFA del owner**: crear el tenant no la exige, pero entrar en él sí, porque el
   `owner` del catálogo de ejemplo la requiere (12 h). Sin factor:
   `403 auth.mfa_enrollment_required`. Con WorkOS se resuelve como en "Prueba manual: MFA de
   un rol"; aquí, solo en local, se simula una verificación reciente como superusuario.

   ```sh
   api GET /tenant "$ANA" "$T"
   psql_su -v token="$ANA" <<< "UPDATE sessions SET mfa_auth_time = now() WHERE token_hash = sha256(convert_to(:'token', 'UTF8'))"
   api GET /tenant "$ANA" "$T"
   ```

4. **Miembros**: Bea y Carla entran como `member`. Es preparación: hasta las
   invitaciones (paso 5b) no hay otra forma de añadir a alguien. Después, la lista
   paginada de 2 en 2: la primera página trae `next_cursor`, la segunda no.

   ```sh
   psql_su -v t="$T" -v bea="$BEA_ID" -v carla="$CARLA_ID" <<< "INSERT INTO memberships (tenant_id, user_id, role) VALUES (:'t', :'bea', 'member'), (:'t', :'carla', 'member')"
   api GET '/tenant/members?limit=2' "$ANA" "$T"
   CURSOR=$(curl -s 'localhost:8080/tenant/members?limit=2' -H "Cookie: __Host-session=$ANA" -H "X-Tenant-ID: $T" | sed -E 's/.*"next_cursor":"([^"]*)".*/\1/')
   api GET "/tenant/members?limit=2&cursor=$CURSOR" "$ANA" "$T"
   ```

5. **Cambiar rol y jerarquía**: Ana hace `admin` a Bea (`200`). Bea, ya admin, no puede
   tocar a Ana (`403 membership.target_forbidden`) ni nombrar owner a Carla (`403
   membership.role_not_assignable`); Carla, `member`, no puede gestionar (`403 authz.denied`).
   Nadie se cambia el rol a sí mismo (`403 membership.self_change`). Un usuario que no es
   miembro de este tenant da `404 membership.not_found`, exista o no.

   ```sh
   api PATCH "/tenant/members/$BEA_ID" "$ANA" "$T" '{"role":"admin"}'
   api PATCH "/tenant/members/$ANA_ID" "$BEA" "$T" '{"role":"member"}'
   api PATCH "/tenant/members/$CARLA_ID" "$BEA" "$T" '{"role":"owner"}'
   api PATCH "/tenant/members/$BEA_ID" "$CARLA" "$T" '{"role":"member"}'
   api PATCH "/tenant/members/$ANA_ID" "$ANA" "$T" '{"role":"admin"}'
   api PATCH "/tenant/members/0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f9999" "$ANA" "$T" '{"role":"admin"}'
   ```

6. **Expulsar, con efecto inmediato**: Bea expulsa a Carla (`204`); la siguiente petición
   de Carla, con la misma cookie y el mismo `X-Tenant-ID`, ya da `403 auth.tenant_forbidden`.

   ```sh
   api DELETE "/tenant/members/$CARLA_ID" "$BEA" "$T"
   api GET /tenant "$CARLA" "$T"
   ```

7. **Dejar el tenant sin owner**: Ana es la única owner. No puede salir (`409
   membership.last_owner`). Bea sí puede salir (`204`).

   ```sh
   api DELETE /tenant/members/me "$ANA" "$T"
   api DELETE /tenant/members/me "$BEA" "$T"
   ```

8. **La invariante la guarda la BD**, como `app_runtime` con las variables que fija
   `db.WithTenantTx`: ni un `DELETE` ni un `UPDATE` directos sobre `memberships` (`permission
   denied`), y `leave_tenant()` de la última owner falla con `TN061`.

   ```sh
   for sql in "DELETE FROM memberships WHERE user_id = :'u'" \
              "UPDATE memberships SET role = 'admin' WHERE user_id = :'u'" \
              "SELECT leave_tenant()"; do
     psql_app -v t="$T" -v u="$ANA_ID" 2>&1 <<SQL | grep -E 'ERROR|leave_tenant' | head -1
   BEGIN;
   SELECT set_config('app.tenant_id', :'t', true), set_config('app.user_id', :'u', true),
          set_config('app.scope', 'tenant', true) \g /dev/null
   $sql;
   ROLLBACK;
   SQL
   done
   ```

9. **Auditoría**, como `app_runtime` en el tenant: cada acción con su actor, su
   destinatario y sus roles. Los intentos rechazados por la jerarquía no escriben nada
   (`authz.denied` sí: es el de Carla).

   ```sh
   psql_app -v t="$T" -v u="$ANA_ID" <<'SQL'
   BEGIN;
   SELECT set_config('app.tenant_id', :'t', true), set_config('app.user_id', :'u', true),
          set_config('app.scope', 'tenant', true) \g /dev/null
   SELECT a.action, u.email AS actor, a.target_id = :'u' AS target_is_ana, a.metadata - 'membership_id' AS metadata
   FROM audit_log a LEFT JOIN users u ON u.id = a.actor_user_id ORDER BY a.created_at;
   COMMIT;
   SQL
   ```

10. **Limpieza**, como `factory`. El registro de auditoría es inmutable para la aplicación,
    y un tenant no se borra (sección 12 del diseño): en local, el superusuario borra todo.

    ```sh
    psql_su -v t="$T" <<'SQL'
    DELETE FROM audit_log WHERE tenant_id = :'t';
    DELETE FROM memberships WHERE tenant_id = :'t';
    DELETE FROM tenants WHERE id = :'t';
    DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE email LIKE '%.runbook@example.test');
    DELETE FROM audit_log WHERE actor_user_id IN (SELECT id FROM users WHERE email LIKE '%.runbook@example.test');
    DELETE FROM users WHERE email LIKE '%.runbook@example.test';
    SQL
    ```

## Prueba manual: invitaciones

Diseño: 5.6 y 5.7. Solo en local. Hasta que llegue el email real, el adaptador de
desarrollo escribe el enlace en el log de la API, y es la única vía de leer un token. La
API no arranca con él fuera de desarrollo.

Mismas reglas que en la sección anterior: **preparar** (sesiones, simular la MFA de la
owner) con `factory`; **comprobar** con la API o con `app_runtime`. Usa sus funciones
`psql_su`, `psql_app`, `api` y `new_session` (sus bloques de ayuda y del paso 1).

Arranca la API guardando su log, y desde la raíz del repo en otra terminal:

```sh
mise run dev 2>&1 | tee /tmp/factory-api.log     # terminal 1
```

```sh
API_LOG=${API_LOG:-/tmp/factory-api.log}
# El enlace de la última invitación enviada, y su token. El email va enmascarado en el log.
last_link()  { grep '"invitation link (development only)"' "$API_LOG" | tail -1 | sed -E 's/.*"link":"([^"]*)".*/\1/'; }
last_token() { last_link | sed 's/.*#token=//'; }
id_of()      { sed -nE '1s/.*"id":"([^"]*)".*/\1/p'; }   # api adds a status line
```

1. **Usuarias y tenant**: Ana crea el tenant (es owner; se simula su MFA como en la
   sección anterior); Bea, Carla y Eva solo tienen sesión.

   ```sh
   ANA=$(new_session ana.runbook@example.test);   BEA=$(new_session bea.runbook@example.test)
   CARLA=$(new_session carla.runbook@example.test); EVA=$(new_session eva.runbook@example.test)
   BEA_ID=$(psql_su <<< "SELECT id FROM users WHERE email = 'bea.runbook@example.test'")
   api POST /tenants "$ANA" "" '{"name":"Invitaciones","slug":"invitaciones-runbook"}'
   T=$(psql_su <<< "SELECT id FROM tenants WHERE slug = 'invitaciones-runbook'")
   psql_su -v token="$ANA" <<< "UPDATE sessions SET mfa_auth_time = now() WHERE token_hash = sha256(convert_to(:'token', 'UTF8'))"
   ```

2. **Invitar y ver el enlace en el log**: Ana invita a Bea como `admin` (`201`, sin token
   en la respuesta). El enlace aparece en el log de la API con el email enmascarado.

   ```sh
   api POST /invitations "$ANA" "$T" '{"email":"bea.runbook@example.test","role":"admin"}'
   grep '"invitation link (development only)"' "$API_LOG" | tail -1
   ```

3. **Aceptar con otra usuaria y con la buena**: Carla no puede usar el enlace de Bea (`403
   invitation.email_mismatch`); Bea sí (`200`, con el `tenant_id` y el rol), y una segunda
   vez da `409 invitation.already_accepted`.

   ```sh
   BEA_TOKEN=$(last_token)
   api POST /invitations/accept "$CARLA" "" "{\"token\":\"$BEA_TOKEN\"}"
   api POST /invitations/accept "$BEA" "" "{\"token\":\"$BEA_TOKEN\"}"
   api POST /invitations/accept "$BEA" "" "{\"token\":\"$BEA_TOKEN\"}"
   ```

4. **Duplicados y reglas**: Bea, `admin`, no puede invitar a un `owner` (`403
   invitation.role_not_offerable`). Invita a Carla y a Eva como `member`; repetir a Eva da
   `409 invitation.pending`, e invitar a Bea, que ya es miembro, `409 invitation.already_member`.

   ```sh
   api POST /invitations "$BEA" "$T" '{"email":"x.runbook@example.test","role":"owner"}'
   api POST /invitations "$BEA" "$T" '{"email":"carla.runbook@example.test","role":"member"}'
   CARLA_TOKEN=$(last_token)
   EVA_INV=$(api POST /invitations "$BEA" "$T" '{"email":"eva.runbook@example.test","role":"member"}' | id_of)
   EVA_OLD=$(last_token)
   api POST /invitations "$BEA" "$T" '{"email":"EVA.runbook@example.test","role":"member"}'
   api POST /invitations "$ANA" "$T" '{"email":"bea.runbook@example.test","role":"member"}'
   ```

5. **Reenviar**: Ana reenvía la de Eva (`200`). Hay un enlace nuevo en el log y el antiguo
   ya no vale (`404 invitation.not_found`).

   ```sh
   api POST "/invitations/$EVA_INV/resend" "$ANA" "$T"
   EVA_NEW=$(last_token); [ "$EVA_NEW" != "$EVA_OLD" ] && echo "token rotado"
   api POST /invitations/accept "$EVA" "" "{\"token\":\"$EVA_OLD\"}"
   ```

6. **Revocar**: Ana invita a otra dirección y la revoca (`204`). Revocarla otra vez da
   `409 invitation.revoked`. La lista de pendientes (`GET /invitations`) solo trae las de
   Carla y Eva.

   ```sh
   X_INV=$(api POST /invitations "$ANA" "$T" '{"email":"x.runbook@example.test","role":"member"}' | id_of)
   api DELETE "/invitations/$X_INV" "$ANA" "$T"
   api DELETE "/invitations/$X_INV" "$ANA" "$T"
   api GET /invitations "$ANA" "$T"
   ```

7. **Degradar a quien invitó revoca sus pendientes**: Carla acepta la suya antes. Ana
   degrada a Bea a `member`, que no tiene `members:invite`. La invitación pendiente de Eva,
   emitida por Bea, queda revocada en la misma transacción, y su enlace nuevo da `409
   invitation.revoked`.

   ```sh
   api POST /invitations/accept "$CARLA" "" "{\"token\":\"$CARLA_TOKEN\"}"
   api PATCH "/tenant/members/$BEA_ID" "$ANA" "$T" '{"role":"member"}'
   api POST /invitations/accept "$EVA" "" "{\"token\":\"$EVA_NEW\"}"
   ```

8. **Comprobación como `app_runtime`**, con las variables de `db.WithTenantTx`: el estado de
   cada invitación y la auditoría. Ningún evento lleva el email ni el token.

   ```sh
   psql_app -v t="$T" -v u="$BEA_ID" <<'SQL'
   BEGIN;
   SELECT set_config('app.tenant_id', :'t', true), set_config('app.user_id', :'u', true),
          set_config('app.scope', 'tenant', true) \g /dev/null
   SELECT split_part(email::text, '@', 1) AS email, role,
          CASE WHEN accepted_at IS NOT NULL THEN 'aceptada' WHEN revoked_at IS NOT NULL THEN 'revocada'
               ELSE 'pendiente' END AS estado
   FROM invitations ORDER BY created_at;
   SELECT a.action, split_part(u.email::text, '@', 1) AS actor, a.metadata->>'role' AS role,
          a.metadata->>'reason' AS reason, a.metadata::text LIKE '%@%' AS has_email
   FROM audit_log a LEFT JOIN users u ON u.id = a.actor_user_id
   WHERE a.action LIKE 'invitation.%' ORDER BY a.created_at;
   COMMIT;
   SQL
   ```

9. **Limpieza**, como `factory`, igual que en la sección anterior, con el slug
   `invitaciones-runbook`:

   ```sh
   psql_su -v t="$T" <<'SQL'
   DELETE FROM audit_log WHERE tenant_id = :'t';
   DELETE FROM invitations WHERE tenant_id = :'t';
   DELETE FROM memberships WHERE tenant_id = :'t';
   DELETE FROM tenants WHERE id = :'t';
   DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE email LIKE '%.runbook@example.test');
   DELETE FROM audit_log WHERE actor_user_id IN (SELECT id FROM users WHERE email LIKE '%.runbook@example.test');
   DELETE FROM users WHERE email LIKE '%.runbook@example.test';
   SQL
   ```

## Prueba manual: MFA de un rol

Con WorkOS de verdad (opción A de la sección anterior: sesión del navegador) y una app de
autenticación (Google Authenticator, 1Password…). Usa `SESSION`, `MIO` y `psql_su` de la
sección anterior, con tu usuario como `owner` de `prueba` (el `owner` del catálogo de
ejemplo exige MFA de menos de 12 h):

```sh
psql_su -v email="$EMAIL" <<< "UPDATE memberships SET role = 'owner' WHERE tenant_id = (SELECT id FROM tenants WHERE slug = 'prueba') AND user_id = (SELECT id FROM users WHERE email = :'email')"
```

1. Sin factor: `GET /tenant` da `403 auth.mfa_enrollment_required`.

   ```sh
   curl -s localhost:8080/tenant -H "Cookie: __Host-session=$SESSION" -H "X-Tenant-ID: $MIO"; echo
   ```

2. Alta: guarda `factor_id` y `challenge_id`, y escanea el `qr_code` (es un `data:` URI:
   pégalo en la barra del navegador) o introduce `secret` en la app.

   ```sh
   curl -s -X POST localhost:8080/me/mfa/totp -H "Cookie: __Host-session=$SESSION" \
     -H "Origin: http://localhost:5173"; echo
   ```

3. Verifica con el código de la app. La respuesta trae un `Set-Cookie` nuevo: usa ese
   valor como `SESSION` a partir de aquí. La cookie anterior ya da `401`.

   ```sh
   curl -s -i -X POST localhost:8080/me/mfa/totp/verify -H "Cookie: __Host-session=$SESSION" \
     -H "Origin: http://localhost:5173" -H "Content-Type: application/json" \
     -d '{"challenge_id":"<challenge_id>","code":"<6 dígitos>"}' | grep -iE '^(HTTP|set-cookie)'
   ```

4. `GET /tenant` con la cookie nueva: `200`. `GET /me` trae `mfa_enrolled: true`,
   `session_mfa_at` y `mfa_required: true` en la membresía de `prueba`.
5. Repetir el mismo código con un challenge nuevo (`POST /me/mfa/challenges` con
   `{"factor_id": "…"}`) da `409 mfa.code_reused`; un código erróneo, `422
   mfa.code_invalid`; el sexto intento en 15 minutos, `429 mfa.rate_limited`.
6. Baja del único factor siendo `owner`: `403 mfa.last_factor_required`.

   ```sh
   curl -s -X DELETE localhost:8080/me/mfa/totp/<factor_id> -H "Cookie: __Host-session=$SESSION" \
     -H "Origin: http://localhost:5173"; echo
   ```

7. **¿Doble petición de código?** Con el panel ya sin `Required` y este usuario con un
   factor verificado, cierra sesión (`POST /auth/logout` o **Salir**) y entra otra vez en
   una ventana privada. Los factores se dan de alta con la API de usuarios de AuthKit
   (`/user_management/users/{id}/auth_factors`), y WorkOS no documenta si AuthKit pide su
   código en el login cuando la MFA no es `Required` (diseño, sección 13). Anota qué pasa:
   - **AuthKit no pide el código**: lo esperado. Solo la app lo pide, y solo cuando un rol
     lo exige.
   - **AuthKit pide el código TOTP**: el usuario lo teclea dos veces, en el login y en la
     app. La app sigue siendo correcta, porque no se fía de ese primero, pero es una
     molestia. Anótalo (fecha, entorno, método de login, captura) en la sección 13 del
     diseño y abre una issue para decidir. Opciones: aceptarlo; dar de alta los factores
     con la API de MFA independiente (`/auth/factors/enroll`, sin `user_id`), que AuthKit
     no asocia al usuario; o pedir el código en la app solo si el login no lo pidió, lo
     que exigiría una señal que WorkOS no da.

   Repite la prueba con cada método de login que use la app (contraseña, Magic Auth,
   Google…): el comportamiento puede cambiar de uno a otro.
8. En la auditoría, como `app_admin`: `mfa.factor_enrolled`, `mfa.verified`,
   `mfa.verify_failed` (con `reason`) y `mfa.verify_locked`.
9. Limpieza: borra el factor como en "Recuperación" (pasos 2 y 3) y los tenants como en
   la sección anterior.
