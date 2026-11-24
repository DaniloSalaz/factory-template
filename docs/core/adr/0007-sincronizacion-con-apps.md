# ADR 0007 — Sincronización entre la plantilla y sus apps

- Estado: aceptado
- Fecha: 2026-11-24

## Contexto
Cada app nace de factory-template y tiene que seguir recibiendo las mejoras del core
(seguridad, identidad, CI, reglas de arquitectura) durante toda su vida. Una app y la
plantilla tocan zonas distintas del repo ([estructura](../estructura.md): [P] core, [A] app,
[G] generado, [C] compartido), así que el mecanismo tiene que traer los cambios de [P],
fusionar los de [C] y no pisar nunca los de [A]. Hay un único desarrollador y pocas apps:
lo que se elija tiene que funcionar con herramientas que ya están en el stack y sin
infraestructura nueva.

## Decisión
La app nace **clonando** la plantilla y recibe sus cambios con un **remoto `template` y
`git merge`**:

- `mise run template:init`, en el clon recién hecho, renombra `origin` a `template` y
  escribe `.template-version` con el commit de la plantilla del que parte la app.
- `mise run template:sync`, en una rama, hace `git fetch template` y
  `git merge --no-ff template/main`, actualiza `.template-version` y regenera el código.
  Los conflictos (solo deberían aparecer en ficheros [C]) se resuelven a mano: no se fuerza
  ninguna estrategia de merge (`-X ours`/`-X theirs`), que resolvería en silencio a favor de
  un lado.
- `.template-paths` (de la plantilla) lista las rutas [P]. `mise run template:check`, en el
  CI de la app, falla si alguna difiere entre el commit de `.template-version` y el árbol
  de trabajo: un cambio de core hecho desde una app se ve en el PR, no en el siguiente sync.
  En la plantilla, sin `.template-version`, no comprueba nada.
- El `CHANGELOG.md` de la plantilla describe cada versión con su sección «Cómo migrar», que
  se lee antes de sincronizar ([runbook](../runbooks/template-sync.md)).

El commit de `.template-version` siempre está en la historia de la app (es un ancestro de
un merge o del clon), así que `template:check` no necesita acceso al repo de la plantilla:
basta un checkout con la historia completa (`fetch-depth: 0`).

## Alternativas descartadas
- **El botón «Use this template» de GitHub**: crea un repo con un único commit nuevo y sin
  ancestro común con la plantilla. Cada sync sería un merge de historias sin relación
  (`--allow-unrelated-histories`) en el que todos los ficheros entran en conflicto, o un
  `cherry-pick` a mano de cada commit de la plantilla.
- **Copier** (o cookiecutter con `cruft`): genera la app desde una plantilla con preguntas y
  la actualiza con un diff de tres vías entre versiones renderizadas. Sirve cuando el
  proyecto generado varía según las respuestas; aquí no varía nada (el module path de Go es
  neutro y la identidad de la app va en configuración), así que añadiría Python, una
  sintaxis de plantilla (Jinja) en ficheros de código que entonces ya no compilan tal cual
  en la plantilla, y un formato de conflicto distinto del de git, a cambio de nada.
- **Paquetes versionados** (el core como módulo de Go y paquete de npm de los que dependen
  las apps): es la evolución natural cuando el core se estabilice ([ADR 0006](0006-zona-core.md)
  ya prevé esa librería). Hoy el core cambia en casi cada PR y no solo en código: también
  migraciones, contrato, workflows, reglas de lint, tests de arquitectura y documentación,
  que no se distribuyen como dependencia. Publicar y versionar cada cambio en dos registros,
  más una API pública estable de `core/`, cuesta más de lo que ahorra con un solo
  desarrollador. Se revisará cuando el core deje de cambiar a menudo.

## Consecuencias
- Una app tiene la historia completa de la plantilla, y `git log template/main` y
  `git diff <.template-version> template/main` muestran qué llega en el próximo sync.
- Los ficheros [C] (`mise.toml`, `README.md`, `compose.yaml`, `api/go.mod`,
  `web/package.json`, `api/sqlc.yaml`…) dan conflictos normales de git. Por eso se mantienen
  al mínimo, y `api/go.mod` pasa de [P] a [C]: una app tiene que poder añadir dependencias
  de Go (el module path `chaskicode.dev/saas` sigue sin renombrarse nunca).
- Un cambio de core que necesite una app se hace en la plantilla y llega con
  `template:sync`; mientras tanto, la app lo apunta en `FACTORY_NOTES.md`.
- No cubre (todavía): que una app no haya sincronizado en mucho tiempo (nadie avisa), ni
  subir automáticamente un cambio de una app a la plantilla.
