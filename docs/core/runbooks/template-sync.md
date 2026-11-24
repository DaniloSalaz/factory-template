# Runbook: sincronización con la plantilla

Cómo nace una app de factory-template y cómo recibe después las mejoras del core. La
decisión y sus alternativas, en el [ADR 0007](../adr/0007-sincronizacion-con-apps.md); qué
es de quién ([P], [A], [C], [G]), en [estructura](../estructura.md).

| Fichero | Dónde | Qué es |
|---|---|---|
| `.template-paths` | plantilla (llega a las apps) | Las rutas [P]: una ruta o glob por línea, `dir/` para una carpeta entera y `!` para excluir |
| `.template-version` | solo en las apps | El commit de la plantilla del último sync |
| `CHANGELOG.md` | plantilla | Qué trae cada versión y cómo migrar a ella |
| `FACTORY_NOTES.md` | cada app | Fricciones con el core y candidatos a subir a la plantilla |

| Tarea | Qué hace |
|---|---|
| `mise run template:init` | En un clon nuevo: `origin` pasa a llamarse `template` y escribe `.template-version` con el commit actual. Falla si ya existe `.template-version` |
| `mise run template:sync` | En una rama que no sea `main` y con el árbol limpio: `git fetch template`, `git merge --no-ff template/main`, actualiza `.template-version` y ejecuta `mise run generate` |
| `mise run template:check` | En la plantilla pasa sin comprobar nada; en una app falla si una ruta de `.template-paths` difiere del commit de `.template-version` y lista los ficheros. Va en `mise run ci` y en el job `template` del CI |

## Crear una app nueva

Con la shell fish. La app se crea **clonando** la plantilla, nunca con el botón «Use this
template» de GitHub: sin historia común, cada sync sería un conflicto en todos los
ficheros.

```fish
set app mi-app
set owner mi-usuario
git clone git@github.com:$owner/factory-template.git $app
cd $app
mise trust; and mise install; and mise run hooks:install
mise run template:init
gh repo create $owner/$app --private
git remote add origin git@github.com:$owner/$app.git
git push -u origin main                 # la historia de la plantilla, tal cual
git switch -c chore/template-init
git add .template-version
git commit -m "chore: inicia la app desde factory-template"
git push -u origin chore/template-init  # y abre el PR
```

`template:init` también se puede ejecutar en la plantilla por error (no tiene forma de
distinguirla de un clon recién hecho): se deshace con `rm .template-version` y
`git remote rename template origin`.

Después, en el PR de inicio o en los siguientes: `APP.md` (qué es la app), `docs/app/`
(problema y especificación), la marca en `web/src/app/theme.css` y `web/src/app/brand/`, y
los secretos propios (`secrets.env`, `.sops.yaml`). Todo eso es [A]; nada de `.template-paths`.

## Antes de sincronizar: el CHANGELOG

Lo que trae el próximo sync, desde el último:

```fish
git fetch template
git log --oneline --no-merges (cat .template-version)..template/main
git diff (cat .template-version) template/main -- CHANGELOG.md
```

Lee la sección **«Cómo migrar»** de cada versión nueva: dice qué tiene que cambiar en
`app/` (un import renombrado, un permiso nuevo en `app.Roles`, una migración de app que
depende de una de core…) y qué ficheros [C] es probable que choquen. Esos cambios de `app/`
van en el mismo PR del sync, para que el CI lo valide todo junto.

## Sincronizar

```fish
git switch main; and git pull
git switch -c chore/template-sync
mise run template:sync
mise run ci
git push -u origin chore/template-sync   # y abre el PR
```

Sin conflictos, `template:sync` deja un único commit de merge
(`chore: sincroniza con la plantilla (<commit>)`) que incluye `.template-version` y el
código regenerado, y termina con `template:check` en verde. Si ya estás al día, no hace
nada.

## Resolver conflictos

Un conflicto es un conflicto normal de git: `template:sync` se para, deja el merge a medias
con `.template-version` ya actualizado y lista los pasos. No uses `-X ours`/`-X theirs` ni
otra estrategia para todo el merge: resolvería en silencio a favor de un lado.

- **En un fichero [C]** (`mise.toml`, `README.md`, `compose.yaml`, `.env.example`,
  `api/go.mod`, `api/sqlc.yaml`, `web/package.json`…): conserva los dos cambios. Ejemplo,
  `mise.toml` cuando la plantilla sube la versión de una herramienta y la app la había
  cambiado en la misma línea:

  ```text
  <<<<<<< HEAD
  node = "26.14.0"          # la app
  =======
  node = "26.13.2"          # la plantilla
  >>>>>>> refs/remotes/template/main
  ```

  Decide qué valor vale para los dos (normalmente el más reciente, o el de la plantilla
  si el cambio de la app era provisional), borra los marcadores y deja una sola línea. Si
  el cambio de la app es algo que la plantilla también debería tener, apúntalo en
  `FACTORY_NOTES.md` («Candidatos a subir al core»). En `api/go.mod` y
  `web/package-lock.json` resuelve el manifiesto y regenera el resto (`cd api; and go mod tidy`,
  `cd web; and npm install`) en vez de editar `go.sum` o el lockfile a mano.
- **En una ruta de `.template-paths`**: no debería pasar (`template:check` impide editar el
  core desde la app). Si pasa, gana la plantilla: `git checkout --theirs -- <fichero>`.
- **En una ruta [A]**: la plantilla solo trae ejemplos ahí (`api/app/app.go`,
  `web/src/app/registry.ts`, `docs/app/`); conserva lo de la app y añade lo que pida «Cómo
  migrar».
- **En código generado [G]**: no lo resuelvas a mano; quédate con cualquiera de los dos
  lados y regenera.

Para terminar el merge:

```fish
git add <ficheros resueltos>
mise run generate; and git add api/gen web/src/gen api-spec/openapi.gen.yaml
git commit --no-edit
mise run template:check
```

Para abandonarlo: `git merge --abort` (deja la rama como estaba, `.template-version`
incluido).

## Si el core necesita un cambio

Una ruta de `.template-paths` no se edita nunca desde una app, ni siquiera de forma
provisional: `template:check` lo detecta en el PR y lo lista.

1. Si solo estorba, apúntalo en `FACTORY_NOTES.md` («Fricciones con el core»: fecha, qué
   pasó, por qué estorba, qué esperarías) y sigue con lo de la app. Si bloquea, para.
2. Haz el cambio en factory-template, con su PR, su test y, si cambia algo que la app
   tiene que adaptar, su entrada en `CHANGELOG.md` con «Cómo migrar».
3. Cuando esté fusionado en la plantilla, sincroniza la app y mueve la entrada de
   `FACTORY_NOTES.md` a «Resuelto» con el enlace al PR o ADR.

Si `template:check` falla por un cambio hecho sin querer, revierte el fichero a la versión
de la plantilla:

```fish
git restore --source=(cat .template-version) -- <fichero>
```

## Lo que no cubre todavía

- Nadie avisa de que una app lleva mucho sin sincronizar: hay que mirar
  `git log (cat .template-version)..template/main` de vez en cuando.
- Subir algo de una app a la plantilla es a mano (se copia el código en un PR de
  factory-template).
- `template:check` compara rutas, no contenido semántico: un fichero [C] puede romper el
  core (una versión de herramienta incompatible en `mise.toml`) sin que lo detecte; eso lo
  encuentra `mise run ci`.
- Las versiones del `CHANGELOG.md` no son etiquetas de git: el sync siempre trae
  `template/main` entero.
