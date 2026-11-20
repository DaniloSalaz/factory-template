# Runbook: base de datos

## Roles

Los crea `infra/db/bootstrap.sql`, no las migraciones: los roles son del clúster y
`BYPASSRLS` exige superusuario.

| Rol | Login | BYPASSRLS | Para qué |
|---|---|---|---|
| `app_owner` | sí | no | Dueño de tablas y funciones mientras se crean; ejecuta las migraciones (`MIGRATIONS_DATABASE_URL`) |
| `app_runtime` | sí | no | La API (`DATABASE_URL`). Nunca es dueño de nada; RLS le aplica siempre |
| `app_security` | no | sí | Dueño de las funciones `SECURITY DEFINER`; solo actúa a través de ellas |
| `app_admin` | sí | sí | Comandos de administración (deshabilitar o borrar usuarios), auditados |

`app_owner` es miembro de `app_security` con `SET` y sin `INHERIT`: puede asignarle la
propiedad de una función (`ALTER FUNCTION … OWNER TO app_security`) sin heredar sus
privilegios en cada consulta. Pero con un `SET ROLE app_security` explícito obtiene
`BYPASSRLS`, y como dueño de las tablas puede desactivar RLS o concederse cualquier
permiso.

**`MIGRATIONS_DATABASE_URL` equivale a acceso total a los datos de todos los tenants.**
Trátala como la credencial de un superusuario: solo la reciben las migraciones, nunca la
API, los workers ni los logs.

## Bootstrap

El script es idempotente y recibe las contraseñas como variables de psql. Crea:
- el esquema `extensions` con `citext`, `pgcrypto` y `pg_trgm`;
- los cuatro roles, con sus atributos y contraseñas;
- los permisos sobre la base de datos y los esquemas;
- el `search_path` de cada rol (`public, extensions`).

- **Local**: `compose.yaml` monta `infra/db/initdb.sh` en `/docker-entrypoint-initdb.d`,
  y la imagen lo ejecuta **solo cuando el volumen está vacío**. Con un volumen anterior a
  los roles, recréalo con `docker compose down -v && mise run up && mise run migrate`, o
  ejecútalo a mano con `mise run db:bootstrap`.
- **Tests**: `api/internal/platform/db/dbtest` arranca Postgres 18 con el mismo
  `initdb.sh` y el mismo `bootstrap.sql`, y migra como `app_owner`.
- **Otros entornos**: un superusuario lo ejecuta una vez, y otra vez cada vez que cambie el script:

  ```sh
  psql "$SUPERUSER_URL" -v ON_ERROR_STOP=1 \
    -v app_owner_password="…" -v app_runtime_password="…" -v app_admin_password="…" \
    -f infra/db/bootstrap.sql
  ```

  Las contraseñas salen del gestor de secretos (SOPS), nunca de un fichero del repo.

## Permisos en tablas nuevas

No hay `ALTER DEFAULT PRIVILEGES` para tablas: una tabla nueva no es accesible hasta que
su migración dice quién la usa y cómo. En la misma migración que crea la tabla:

1. Si tiene `tenant_id`: `ENABLE` y `FORCE ROW LEVEL SECURITY`, la política
   `tenant_isolation` y su alcance declarado (diseño, sección 7.3). El test de esquema
   (`api/internal/platform/db/schematest`) falla si falta algo.
2. `GRANT` a `app_runtime` solo de los privilegios que la aplicación necesita (por ejemplo,
   sin `DELETE` en tablas de solo inserción). `UPDATE` siempre por columnas
   (`GRANT UPDATE (col, …)`), nunca de toda la tabla. Añade los permisos nuevos a
   `api/internal/platform/db/schematest/testdata/runtime_privileges.golden`, a mano, y
   justifícalos en el PR: es una decisión de seguridad, y el test falla mostrando el diff
   hasta que el fichero coincide.
3. `GRANT` a `app_admin` si los comandos de administración la tocan.
4. Si una función `SECURITY DEFINER` la usa, `GRANT` a `app_security` de lo justo.

Las funciones sí tienen un default: `00001_init.sql` revoca el `EXECUTE` que Postgres
concede a `PUBLIC` en cada función nueva. Cada función se concede explícitamente.
