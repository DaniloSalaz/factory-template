# Runbook: login con WorkOS AuthKit

Diseño: `docs/designs/auth-y-tenancy.md` (secciones 5.1, 5.2, 5.5, 6 y 13).

## Variables

| Variable | Dónde | Notas |
|---|---|---|
| `WORKOS_CLIENT_ID` | `[env]` de `mise.toml` (o el entorno) | Empieza por `client_` |
| `WORKOS_API_KEY` | `secrets.env`, cifrado con SOPS + age | Nunca en `mise.toml`, `.env` ni logs (`pii.Secret`) |
| `WORKOS_REDIRECT_URI` | `[env]` | Registrada como Redirect URI en el panel; `https` fuera de desarrollo |
| `WORKOS_MFA_REQUIRED` | `[env]` | `true` solo si el panel tiene la MFA en **Required** (ver abajo) |
| `FRONTEND_ORIGIN` | `[env]` | Origen al que debe resolver `return_to` |
| `ALLOWED_ORIGINS` | `[env]` | Lista cerrada para CORS y CSRF; incluye `FRONTEND_ORIGIN`; sin comodines |
| `TRUSTED_CLIENT_IP_HEADER` | `[env]` | Vacía en desarrollo; `CF-Connecting-IP` por defecto en staging y producción; nunca `X-Forwarded-For` |
| `SESSION_IDLE_TTL` / `SESSION_ABSOLUTE_TTL` | `[env]` | 168h y 720h por defecto |

### Secretos con SOPS

mise no descifra ficheros dotenv cifrados con SOPS: los cargaría con el texto cifrado
(`ENC[…]`) como valor. Por eso `mise run dev` ejecuta la API con
`sops exec-env ../secrets.env 'go run ./cmd/api'`, y `secrets.env` **no** se declara en
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
3. **Authentication → Features → Multi-factor auth**: `Required` en los entornos de las
   apps con roles que exigen MFA.

## MFA: comprobación manual obligatoria en cada entorno

WorkOS no informa de si hubo MFA: el access token no trae `amr` ni nada equivalente y
la respuesta de autenticación solo da `authentication_method` (`Password`, `GoogleOAuth`,
`SSO`…). La app solo puede **deducirlo** de la configuración (diseño, 5.2 y 13):

- cada login fuerza una autenticación nueva (`max_age=0`), así que `auth_time` es la
  hora real de esa autenticación;
- si `WORKOS_MFA_REQUIRED=true` y el método está en la **lista blanca** de métodos que
  piden el segundo factor, la sesión guarda `mfa_auth_time = auth_time`; con cualquier
  otro método, también los desconocidos, `NULL`. Lista actual: `Password`;
- si algún rol del catálogo exige MFA y `WORKOS_MFA_REQUIRED=false`, la API no arranca.

Si `WORKOS_MFA_REQUIRED=true` pero el panel no la exige, la app trataría como MFA
logins que no la tuvieron. **Al crear cada entorno, y después de cada cambio en el
panel**, comprueba:

1. Panel del entorno → Authentication → Features → Multi-factor auth: ¿`Required`?
2. Variable `WORKOS_MFA_REQUIRED` desplegada en ese entorno: ¿coincide (`true` ⇔ Required)?
3. Entra con un usuario de prueba sin SSO: AuthKit debe pedir el código TOTP.
4. En los logs de la API (nivel debug), el registro `workos access token claims` lista
   los nombres de los claims del token. Si algún día aparece `amr` (o un claim de MFA),
   avisa: el diseño pide comprobarlo en lugar de deducirlo (sección 13).

### Añadir un método a la lista blanca de MFA

La lista está en `coveredByMFAPolicy`
(`api/internal/modules/identity/adapters/workos/workos.go`). Un método (`MagicAuth`,
`Passkey`, `GoogleOAuth`…) solo entra con una **prueba en staging que demuestre que
AuthKit pide el segundo factor** con ese método:

1. Staging con *Multi-factor auth* en `Required` y `WORKOS_MFA_REQUIRED=true`.
2. Un usuario de prueba con TOTP configurado entra con ese método, en una ventana
   privada (sin sesión de AuthKit).
3. AuthKit pide el código TOTP **antes** de volver al callback. Si no lo pide, o si el
   usuario puede saltárselo, el método no entra.
4. Repite el paso 2 con un usuario sin TOTP configurado: AuthKit debe obligarle a
   configurarlo antes de terminar.
5. El PR que añade el método enlaza la evidencia (capturas o vídeo, con fecha y entorno)
   y añade el método al test `TestExchangeCode_MFAAllowlist` con MFA.

`SSO` nunca entra: WorkOS documenta que su MFA no se aplica a usuarios SSO.

## Prueba manual en desarrollo

Requisitos: Docker, la clave age y el panel de staging configurado como arriba. Usa
Chrome o Firefox: aceptan cookies `Secure` (y el prefijo `__Host-`) en
`http://localhost`; Safari no.

```sh
mise run up && mise run migrate
mise run dev                      # terminal 1: API en :8080
cd web && npm run dev             # terminal 2: front en :5173
```

1. Abre `http://localhost:5173` y pulsa **Entrar**. AuthKit pide credenciales (siempre:
   `max_age=0`) y vuelve a `http://localhost:5173/`.
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

## Prueba manual: `/me`, tenant y `X-Tenant-ID` ajeno

Dos roles, cada uno para lo suyo:
- **Preparar datos** (tenants, usuarios, eventos de prueba): el superusuario local
  `factory`, que salta RLS. Hasta que llegue `POST /tenants` (paso 5) no hay otra forma.
  **Nunca así en staging ni en producción.**
- **Comprobar**: la API, o `app_runtime` con las mismas variables que fija
  `db.WithTenantTx`. Una comprobación hecha como superusuario no prueba nada: se salta RLS.

Desde la raíz del repo, con `mise run up && mise run migrate` y la API en marcha
(`mise run dev`). Los bloques son de bash/zsh.

```sh
SU="docker compose exec -T postgres psql -U factory -d factory -v ON_ERROR_STOP=1 -At"
APP="docker compose exec -T postgres psql -U app_runtime -d factory -v ON_ERROR_STOP=1"
```

1. **Sesión.** Elige una:
   - **A, con navegador**: entra en el front como arriba. Copia el valor de la cookie
     `__Host-session` en `SESSION` y tu email en `EMAIL`.
   - **B, sin navegador** (solo en local): crea un usuario y una sesión de una hora.
     En la BD solo va el hash del token, como en un login real:

     ```sh
     EMAIL=runbook@example.test
     SESSION=$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')
     $SU -v email="$EMAIL" -v token="$SESSION" <<'SQL'
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
   $SU -v email="$EMAIL" <<'SQL'
   INSERT INTO users (email, email_verified, display_name) VALUES ('otra.runbook@example.test', true, 'Otra');
   INSERT INTO tenants (name, slug) VALUES ('Prueba', 'prueba'), ('Ajeno', 'ajeno');
   INSERT INTO memberships (tenant_id, user_id, role)
     SELECT t.id, u.id, 'admin' FROM tenants t, users u WHERE t.slug = 'prueba' AND u.email = :'email'
     UNION ALL
     SELECT t.id, u.id, 'owner' FROM tenants t, users u WHERE t.slug = 'ajeno' AND u.email = 'otra.runbook@example.test';
   INSERT INTO audit_log (tenant_id, action, target_type, target_id)
     SELECT id, 'runbook.seed', 'tenant', slug FROM tenants WHERE slug IN ('prueba', 'ajeno');
   SQL
   MIO=$($SU <<< "SELECT id FROM tenants WHERE slug = 'prueba'")
   AJENO=$($SU <<< "SELECT id FROM tenants WHERE slug = 'ajeno'")
   USER_ID=$($SU -v email="$EMAIL" <<< "SELECT id FROM users WHERE email = :'email'")
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
   $APP -v a="$MIO" -v b="$AJENO" -v u="$USER_ID" <<'SQL'
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
   $APP <<< "SELECT count(*) FROM audit_log;"
   # ERROR:  unrecognized configuration parameter "app.tenant_id"
   ```

8. **Log de acceso**: en la salida de `mise run dev`, los registros `"msg":"http request"`
   de los pasos 3 a 5 llevan `user_id` y `pattern`; el del paso 4, también `tenant_id`.

9. **Limpieza**, como `factory`:

   ```sh
   $SU -v email="$EMAIL" <<'SQL'
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
