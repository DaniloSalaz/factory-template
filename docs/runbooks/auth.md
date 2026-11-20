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
| `TRUSTED_CLIENT_IP_HEADER` | `[env]` | Vacía en desarrollo; `CF-Connecting-IP` por defecto en producción; nunca `X-Forwarded-For` |
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
- si `WORKOS_MFA_REQUIRED=true` y el método no es `SSO` (al que WorkOS no aplica su
  MFA), la sesión guarda `mfa_auth_time = auth_time`; si no, `NULL`;
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
   docker compose exec postgres psql -U app_owner -d factory -c \
     "SELECT id, encode(token_hash, 'hex') AS token_hash, auth_time, mfa_auth_time,
             ip_prefix, provider_session_id IS NOT NULL AS has_sid, revoked_at
        FROM sessions ORDER BY created_at DESC LIMIT 3;"
   ```

   `token_hash` tiene 64 caracteres hex y **no** es el valor de la cookie; el SHA-256 del
   valor de la cookie sí coincide:
   `printf '%s' '<valor de la cookie>' | shasum -a 256`.
4. La auditoría registra el login:
   `SELECT action, actor_user_id, ip_prefix FROM audit_log ORDER BY created_at DESC LIMIT 3;`
5. Pulsa **Salir**: la API revoca la sesión (`revoked_at` deja de ser `NULL`), borra la
   cookie y el navegador pasa por el logout de AuthKit antes de volver al front.
6. Pulsa **Entrar** de nuevo: AuthKit vuelve a pedir credenciales.
