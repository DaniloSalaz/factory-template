# Runbook: email (SMTP)

Las invitaciones salen por SMTP (diseño 5.6.7, adaptador
`api/internal/modules/identity/adapters/smtpsender`). Es un relay SMTP genérico: sirve
cualquier proveedor que dé usuario y contraseña SMTP (Postmark, Amazon SES, Brevo,
Resend, Mailgun, el de tu dominio…). La plantilla no elige proveedor.

## Variables

| Variable | Obligatoria | Valor |
|---|---|---|
| `SMTP_HOST` | Fuera de development | Nombre del servidor, sin esquema ni puerto (`smtp.proveedor.com`). En development, vacía, los enlaces van al log de la API |
| `SMTP_PORT` | Con `SMTP_HOST` | `587` con STARTTLS, `465` con TLS implícito (lo que diga el proveedor) |
| `SMTP_TLS` | No (`starttls`) | `starttls` o `implicit`. No hay modo sin cifrar: si el servidor no ofrece STARTTLS, el envío falla |
| `SMTP_USERNAME`, `SMTP_PASSWORD` | Fuera de development | Credenciales SMTP del proveedor. Van juntas. **La contraseña es un secreto**: en el `secrets.env` del entorno, cifrado con SOPS + age, nunca en `mise.toml`, `.env` ni logs (`pii.Secret`) |
| `SMTP_FROM` | Con `SMTP_HOST` | Remitente, de un dominio con SPF, DKIM y DMARC (abajo): `Acme <no-reply@acme.com>` |
| `SMTP_TIMEOUT` | No (`10s`) | Límite de la conexión y de cada orden SMTP. El envío ocurre en la petición que crea o reenvía la invitación, después del commit |
| `SMTP_TLS_SKIP_VERIFY` | No | Solo en development (para Mailpit). Fuera de development la API no arranca con ella |

La API valida todo al arrancar y, fuera de development, no arranca sin SMTP. No se
conecta al servidor al arrancar: una caída del proveedor no tumba la API. Si un envío
falla, se registra como `Error` (sin enlace ni destinatario) y la invitación queda
creada; el remedio es reenviarla desde la pantalla de invitaciones.

Para añadir la contraseña al fichero de secretos de un entorno:

```sh
export SOPS_AGE_KEY_FILE=~/.config/sops/age/keys.txt
sops edit <fichero de secretos del entorno>     # SMTP_PASSWORD=...
```

## Desarrollo: Mailpit

`mise run up` arranca también Mailpit (`compose.yaml`, solo en `127.0.0.1`), y `mise.toml`
apunta la API a él: STARTTLS obligatorio con un certificado autofirmado que cambia en cada
arranque (por eso `SMTP_TLS_SKIP_VERIFY=true`), y cualquier usuario y contraseña.

```sh
mise run up && mise run migrate && mise run dev
mise run mail        # abre http://localhost:8025: cada invitación llega aquí
```

Para volver al adaptador de log (los enlaces en el log de la API), deja `SMTP_HOST=` vacío
en `.env`.

## DNS del dominio remitente (lo pones tú)

Sin estos registros, los proveedores de correo marcan las invitaciones como spam o las
rechazan. Los valores exactos los da el proveedor SMTP; esto es la forma y el orden.

1. **SPF** (TXT en el dominio de `SMTP_FROM`, p. ej. `acme.com`): autoriza al proveedor a
   enviar por ti. Un solo registro SPF por dominio; si ya hay uno, añade el `include` del
   proveedor a ese.

   ```
   acme.com.  TXT  "v=spf1 include:<spf del proveedor> -all"
   ```

2. **DKIM** (TXT o CNAME que da el proveedor, con su selector): firma cada mensaje.

   ```
   <selector>._domainkey.acme.com.  CNAME  <destino que da el proveedor>
   ```

3. **DMARC** (TXT en `_dmarc`): qué hacer con lo que no pase SPF o DKIM alineados, y adónde
   mandar los informes. Empieza observando y endurece cuando los informes estén limpios:

   ```
   _dmarc.acme.com.  TXT  "v=DMARC1; p=none; rua=mailto:dmarc@acme.com; adkim=s; aspf=s"
   ```

   Después de unas semanas sin fallos legítimos: `p=quarantine` y, por último, `p=reject`.

4. **Return-Path / MAIL FROM** personalizado, si el proveedor lo ofrece (un CNAME tipo
   `bounces.acme.com`): alinea SPF con el dominio del `From` y DMARC pasa también por SPF.

Comprueba con el panel del proveedor (verifica el dominio) y enviando una invitación a una
cuenta de Gmail: en "Mostrar original" deben salir `SPF: PASS`, `DKIM: PASS` y
`DMARC: PASS`.

## Qué no hace el email

- El asunto es fijo y no lleva el nombre del tenant ni el de quien invita: los escriben
  usuarios, y en el asunto servirían para phishing desde nuestro dominio. En el cuerpo van
  escapados, sin saltos de línea y recortados a 80 caracteres.
- El token solo va en el enlace, tras `#token=`. No aparece en logs, errores ni métricas.
