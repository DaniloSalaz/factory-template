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
- **Tests**: `api/platform/kit/db/dbtest` arranca Postgres 18 con el mismo
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
   (`TestSchema` en `api/schema_integration_test.go`, con las reglas de
   `api/platform/kit/db/schematest`) falla si falta algo, también en las tablas de la app. Si es una tabla de acceso
   o una tabla hija, sigue los patrones de `docs/patterns.md`.
2. `GRANT` a `app_runtime` solo de los privilegios que la aplicación necesita (por ejemplo,
   sin `DELETE` en tablas de solo inserción). `UPDATE` siempre por columnas
   (`GRANT UPDATE (col, …)`), nunca de toda la tabla. Añade los permisos nuevos a
   `api/platform/kit/db/schematest/testdata/runtime_privileges.golden`, a mano, y
   justifícalos en el PR: es una decisión de seguridad, y el test falla mostrando el diff
   hasta que el fichero coincide. La foto cubre los permisos de plataforma y de app: la
   compara `TestSchema` sobre una base de datos con los dos conjuntos de migraciones.
3. `GRANT` a `app_admin` si los comandos de administración la tocan.
4. Si una función `SECURITY DEFINER` la usa, `GRANT` a `app_security` de lo justo.

Las funciones sí tienen un default: `00001_init.sql` revoca el `EXECUTE` que Postgres
concede a `PUBLIC` en cada función nueva. Cada función se concede explícitamente.

## Migraciones

Hay dos conjuntos de migraciones goose (`NNNNN_nombre.sql`), cada uno con su numeración
y su tabla de versiones, para que una migración nueva de la plantilla no choque con las
de la app:

| Conjunto | Carpeta | Tabla de versiones | Quién lo cambia |
|---|---|---|---|
| plataforma | `api/platform/migrations/` | `goose_db_version` (la de siempre de goose) | la plantilla |
| app | `api/app/migrations/` | `goose_app_db_version` | cada app |

Los dos van **dentro del binario** (`go:embed`). `api migrate` aplica, con
`MIGRATIONS_DATABASE_URL` (`app_owner`) y nada más de la configuración de la API, primero
las pendientes de plataforma y después las de app, y registra en el log qué versiones
aplicó de cada conjunto (`set`). Si plataforma falla, app no se ejecuta; no arranca el
servidor y sale con código distinto de 0 si algo falla. `mise run migrate` ejecuta lo
mismo en local (`go run . migrate`). La API de migración es `db.Migrate` de
`api/platform/kit/db`, que recibe la lista ordenada de conjuntos (`db.MigrationSet`).

Una nueva:

```sh
mise run migration:new <nombre>            # de la app, en api/app/migrations
mise run migration:new:platform <nombre>   # de plataforma: solo en factory-template
```

Reglas:

- Una migración de app puede usar tablas, funciones y tipos de plataforma; una de
  plataforma **nunca** referencia nada de la app. En una base de datos nueva las de
  plataforma se aplican antes, sin las de app: `TestPlatformMigrationsApplyAlone`
  (`api/platform/migrations`) lo comprueba y falla con `[platform-migrations-alone]`.
- Una migración fusionada en `main` es inmutable; los cambios van en una nueva.
- Sin migraciones de relleno: el conjunto de app vacío (como en la plantilla) aplica
  cero migraciones sin error y solo crea `goose_app_db_version`.

Base de datos existente (creada antes de separar los conjuntos): no hay nada que hacer.
`goose_db_version` sigue siendo la tabla de plataforma, con las mismas versiones, y
`api migrate` dice que no hay migraciones de plataforma pendientes. La primera vez crea
`goose_app_db_version` con solo la fila `version_id = 0` que goose pone en toda tabla
nueva, y aplica cero de app.

Tests: `dbtest.New`/`dbtest.Start` reciben los conjuntos a aplicar. Los módulos de
plataforma pasan `migrations.Set()` de `api/platform/migrations`; los tests de
`platform/kit`, que no pueden importarlo (`[platform-no-modules]`), pasan
`dbtest.PlatformFromSource()`, el mismo conjunto leído del disco (un test comprueba que
son idénticos). Los tests de `package main` (`api/`) aplican plataforma y app, y ahí corren
las reglas de RLS, las funciones `SECURITY DEFINER` y la foto de permisos.
