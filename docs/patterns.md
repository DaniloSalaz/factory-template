# Patrones de tablas de negocio

Patrones obligatorios para las tablas con `tenant_id` de las apps (diseño de auth y
tenancy, sección 7.3). Todos están aplicados en las tablas de ejemplo de
`api/internal/platform/db/testdata/migrations/00001_example_tables.sql`, que pasan el test
de estructura y los tests de comportamiento de `platform/db`.

El test de estructura (`api/internal/platform/db/schematest/rules.go`) comprueba en cada
tabla con `tenant_id`: RLS activo y forzado, `tenant_isolation`, el alcance declarado, las
claves foráneas compuestas hacia otras tablas con `tenant_id` y, en las tablas de acceso,
sus políticas de escritura. Cada mensaje dice qué falta y cómo añadirlo.

## Tabla de acceso

Una tabla que concede acceso a otras filas, como `case_access`, que da a un usuario acceso
a un expediente. Con alcance `own`, un usuario ve sus filas de acceso, pero solo el
alcance `tenant` las crea, modifica o borra. Si no, un cliente podría insertarse una fila
de acceso, o cambiar el `case_id` de la suya, y llegar a cualquier expediente del tenant.

Se marca con `scope: access`. El test de estructura exige entonces `own_scope` y las tres
políticas de escritura, cada una con exactamente `current_setting('app.scope') = 'tenant'`:

```sql
CREATE TABLE case_access (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE RESTRICT,
  case_id    uuid NOT NULL,
  user_id    uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, case_id) REFERENCES cases (tenant_id, id) ON DELETE CASCADE,
  UNIQUE (tenant_id, case_id, user_id)
);
CREATE INDEX case_access_tenant_user_idx ON case_access (tenant_id, user_id, case_id);

ALTER TABLE case_access ENABLE ROW LEVEL SECURITY;
ALTER TABLE case_access FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON case_access AS PERMISSIVE FOR ALL
  USING (tenant_id = current_setting('app.tenant_id')::uuid)
  WITH CHECK (tenant_id = current_setting('app.tenant_id')::uuid);

-- Lectura: cada uno ve sus filas de acceso.
CREATE POLICY own_scope ON case_access AS RESTRICTIVE FOR ALL
  USING (current_setting('app.scope') = 'tenant'
         OR user_id = current_setting('app.user_id')::uuid);

-- Escritura: solo con alcance tenant.
COMMENT ON TABLE case_access IS 'scope: access';
CREATE POLICY own_write_insert ON case_access AS RESTRICTIVE FOR INSERT
  WITH CHECK (current_setting('app.scope') = 'tenant');
CREATE POLICY own_write_update ON case_access AS RESTRICTIVE FOR UPDATE
  USING (current_setting('app.scope') = 'tenant');
CREATE POLICY own_write_delete ON case_access AS RESTRICTIVE FOR DELETE
  USING (current_setting('app.scope') = 'tenant');

GRANT SELECT, INSERT, DELETE ON case_access TO app_runtime;
```

- La política de `UPDATE` es obligatoria aunque no se conceda `UPDATE`: así un permiso
  concedido más adelante no abre el alcance.
- Una fila de acceso no se mueve: sin `UPDATE` para `app_runtime`. Se concede o se borra.
- La tabla cuyo acceso controla (`cases`) usa `own_scope` con un `EXISTS` sobre la tabla
  de acceso. Ese `EXISTS` se ejecuta con las políticas de `case_access`, que ya lo limitan
  a las filas del usuario.
- Al quitar a un miembro, la app borra sus filas de acceso en el manejador de
  `identity.member_removed` (diseño, 5.7).

## Tabla hija: clave foránea compuesta

Toda tabla que referencia a un padre con `tenant_id`, incluidas las tablas de acceso, lo
hace con la clave compuesta `(tenant_id, parent_id)` hacia un `UNIQUE (tenant_id, id)` del
padre. Nunca con una clave simple a `parent(id)`: esa no comprueba el tenant, y una fila
hija del tenant A podría colgar de un padre de B. Con la clave compuesta la base de datos
lo rechaza (`23503`) sea cual sea el código. El test de estructura descubre las claves en
`pg_constraint` y falla si alguna no empareja `tenant_id` con `tenant_id`; las claves
hacia `tenants` y `users`, que no tienen `tenant_id`, quedan exentas.

```sql
CREATE TABLE cases (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE RESTRICT,
  title      text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  -- Destino de las claves compuestas de las hijas; también es el índice por tenant.
  UNIQUE (tenant_id, id)
);

CREATE TABLE case_documents (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE RESTRICT,
  case_id    uuid NOT NULL,
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id, case_id) REFERENCES cases (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX case_documents_tenant_case_idx ON case_documents (tenant_id, case_id);

-- Alcance a través del padre: los documentos de los expedientes con acceso.
CREATE POLICY own_scope ON case_documents AS RESTRICTIVE FOR ALL
  USING (current_setting('app.scope') = 'tenant'
         OR EXISTS (SELECT 1 FROM case_access a
                    WHERE a.tenant_id = case_documents.tenant_id
                      AND a.case_id = case_documents.case_id
                      AND a.user_id = current_setting('app.user_id')::uuid));
```

- El índice de la hija empieza por `(tenant_id, parent_id)`: sirve a la clave foránea y a
  las consultas por padre.
- `ON DELETE CASCADE` o `RESTRICT`, según la app. Las acciones referenciales se ejecutan
  con los permisos del dueño de la tabla y no pasan por RLS.
- La hija también lleva `tenant_isolation`, RLS forzado y su alcance declarado, como
  cualquier tabla con `tenant_id`.
