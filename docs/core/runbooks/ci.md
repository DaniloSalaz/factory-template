# Runbook: CI

El CI (`.github/workflows/ci.yml`) ejecuta las mismas tareas de mise que `mise run ci` en
local, con las versiones fijadas en `mise.toml`. Si algo pasa en local y falla en CI (o al
revés), lo primero es comprobar que `mise install` está al día.

Se ejecuta en cada `pull_request` y en cada `push` a `main`. Las ejecuciones antiguas del
mismo PR se cancelan; las de `main` siempre terminan. Si el cambio solo toca documentación,
los jobs pesados se saltan (ver [Cambios que solo tocan documentación](#cambios-que-solo-tocan-documentación)).

## Jobs

| Job | Qué comprueba | En local |
|---|---|---|
| `changes` | Se ejecuta primero y decide si el cambio toca código (`code=true`) o solo documentación (`code=false`) con `.github/scripts/changes.sh` | `.github/scripts/changes.sh push <base> <head>` |
| `lint` | golangci-lint (incluye las reglas de arquitectura de depguard y forbidigo), actionlint + shellcheck sobre los workflows y los tests de arquitectura | `mise run lint:go`, `mise run lint:actions`, `mise run arch` |
| `test` | Primero los unitarios (`go test -race -cover ./...`, sin Docker) y después los de integración (`-tags integration`), que arrancan Postgres y Mailpit con Testcontainers en el Docker del runner | `mise run test`, `mise run test:integration` (Docker arrancado) |
| `generate` | Une el contrato (`api-spec/core.yaml` + `api-spec/app.yaml` → `api-spec/openapi.gen.yaml`, con `api/tools/specmerge`), regenera sqlc, oapi-codegen (un paquete por `codegen.yaml` de módulo), orval y la seguridad por ruta del front (`web/src/gen/routeSecurity.ts`) y falla si cambia algo en `api/gen`, `web/src/gen` o `api-spec/openapi.gen.yaml` (`gen/openapi/` se regenera desde cero: el paquete de un módulo sin `codegen.yaml` aparece como borrado) | `mise run generate:check` |
| `security` | govulncheck y `trivy fs` (dependencias y Dockerfile), fallando con HIGH o CRITICAL | `mise run security:govulncheck`, `mise run security:trivy` |
| `gitleaks` | gitleaks sobre todo el historial. Se ejecuta siempre, también si solo cambia documentación | `mise run security:gitleaks` |
| `web` | `npm ci`, Biome, `npm run build` (genera `web/src/gen/routeTree.gen.ts` con la CLI de TanStack Router, luego `tsc -b` y `vite build`; falla si el árbol no coincide con el commit) y los tests unitarios con Vitest, incluido el de arquitectura del front | `mise run web:check`, `mise run web:test` |
| `e2e` | El recorrido de Playwright (`web/e2e`) contra la API real: Postgres 18 y Mailpit (el SMTP de desarrollo de `mise.toml`) como servicios, el bootstrap de `infra/db`, `mise run migrate`, la API compilada y arrancada con una `WORKOS_API_KEY` ficticia, y Vite (lo arranca Playwright). Sube las capturas (`web/e2e-screenshots/`) y las trazas (`web/test-results/`, todas en CI) como artefacto `e2e-screenshots-and-traces`; si falla, también el log de la API (`e2e-api-log`) | `mise run up && mise run migrate`, `mise run dev` y `mise run web:e2e` |
| `docker` | Construye la imagen de la API (sin publicarla) y la escanea con Trivy | `mise run docker:scan` |
| `release` (otro workflow) | Tras un push a `main` con el CI en verde y que no sea solo documentación: imagen multiplataforma en GHCR, atestaciones y Trivy de la imagen publicada (`docs/core/runbooks/release.md`) | `mise run docker:build`, `mise run docker:scan` |
| `ci-ok` | Depende de todos los anteriores. Exige `success` en `changes` y `gitleaks`, y en los demás `success` o, solo con `code=false`, `skipped`. Cualquier fallo o cancelación lo hace fallar | — |

El job `e2e` no llama nunca a WorkOS: el arranque de la API solo comprueba que
`WORKOS_API_KEY` existe (la usa para derivar la clave que sella los intentos de login y en
las llamadas de login y MFA), las sesiones se siembran por SQL (`web/e2e/db.ts`, que en CI
entra en el contenedor de servicio con `docker exec`, `E2E_PG_CONTAINER`) y las pantallas de
MFA usan respuestas simuladas. Si un test nuevo necesitara WorkOS, no cabe en este job.

`mise run ci` ejecuta en orden: `lint:go`, `lint:actions`, `arch`, `test`, `test:integration`, `generate:check`,
`security:govulncheck`, `security:gitleaks`, `web:check` y `web:test`. El job `e2e` no está
en `ci` porque necesita Postgres y la API en marcha. Los escaneos de Trivy (`security:trivy`
y `docker:scan`) no están en `ci` porque descargan su base de datos y tardan más; ejecútalos con
`mise run security` y `mise run docker:scan` cuando toques dependencias o el Dockerfile.

## Cambios que solo tocan documentación

El job `changes` compara los ficheros cambiados (`git diff --name-only --no-renames`): en un
PR, desde la base hasta la cabeza del PR (`base...head`, lo que aporta el PR); en un push a
`main`, de `before` a `sha`. El cambio es solo documentación (`code=false`) si **todos** los
ficheros son:

- `docs/**`;
- `*.md` en cualquier carpeta;
- `LICENSE`.

Pero nada bajo `.github/`, `api/`, `web/`, `infra/` ni `api-spec/` es documentación, aunque sea
un `.md` (`web/README.md`, `.github/pull_request_template.md`): las carpetas de código ganan.
Cualquier otro fichero (`mise.toml`, `compose.yaml`, `renovate.json`, `config/`, `tools/`…)
también es código. Con `--no-renames`, mover un fichero de código a `docs/` cuenta el origen
borrado, así que también es código.

Ante cualquier duda, `code=true`: `before` todo ceros (rama nueva), un `git diff` que falla
(un commit que ya no existe), un diff vacío o un evento desconocido. El log del job dice el
rango y qué fichero o motivo lo decidió.

Con `code=false` se saltan `lint`, `test`, `generate`, `security` (govulncheck y trivy), `web`,
`e2e` y `docker`. `gitleaks` se ejecuta siempre: un secreto también puede ir en un `.md`.

| Caso | `ci-ok` |
|---|---|
| Solo documentación | ✅ (pesados saltados) |
| Documentación + código | ✅ si todo pasa (se ejecuta todo) |
| Solo `.github/` | ✅ si todo pasa (se ejecuta todo) |
| Falla `changes` | ❌ (los pesados se saltan sin `code=false`) |
| Falla un job pesado con código | ❌ |
| Falla `gitleaks` | ❌ |
| Un job cancelado, o saltado con `code=true` | ❌ |

`release` no publica imagen si el push a `main` solo cambió documentación. No recalcula el
rango (el evento `workflow_run` no trae `before`): pregunta a la API por el job `docker` de esa
ejecución del CI y, si se saltó, no publica. Con el CI en verde eso solo ocurre con `code=false`.
Si no puede saberlo (error de la API, job ausente), publica.

### Por qué no `paths-ignore`

Con `paths`/`paths-ignore` en `on:`, un PR que solo cambia documentación no ejecutaría el
workflow, `ci-ok` no aparecería nunca y, como es un check obligatorio, el PR quedaría bloqueado
esperándolo. Por eso el workflow se ejecuta siempre y es `ci-ok` quien acepta los saltos. No
añadas filtros de rutas al `on:` de `ci.yml`.

`mise run ci` en local no cambia: siempre ejecuta todo.

## Reglas de arquitectura

| Regla | Dónde se comprueba | Fixture que demuestra que funciona |
|---|---|---|
| `[env-only-in-config]`: `os.Getenv/LookupEnv/Environ` solo en `core/kit/config` | forbidigo | `api/tests/arch/testdata/lint/env-only-in-config` |
| `[platform-no-modules]`: `core/kit` no importa ningún módulo de plataforma (`core/<x>` fuera de `kit`) | depguard | `…/testdata/lint/platform-no-modules` |
| `[apperr-stdlib-only]`: `apperr` solo importa la stdlib | depguard | `…/testdata/lint/apperr-stdlib-only` |
| `[domain-pure]`: `domain` solo importa la stdlib y `core/kit/apperr` | depguard | `…/testdata/lint/domain-pure` |
| `[module-boundaries]`: un módulo es `core/<x>` (salvo `kit`) o `app/modules/<x>`; fuera de él solo se importa su paquete raíz; `gen/openapi/<x>` y `gen/sqlc/<x>` son privados de `<x>` | `api/tests/arch/boundaries_test.go` | `…/testdata/arch` |
| `[platform-imports-app]` (back): nada bajo `api/core/` importa `api/app` ni nada debajo, tampoco de forma indirecta a través de un paquete de fuera de `core/` (un helper de `api/tests/`, `gen/`); solo `package main` importa `app` | `api/tests/arch/appimports_test.go` | `…/testdata/arch` |
| `[tests-not-in-prod]`: ningún fichero que no sea `_test.go` fuera de `api/tests/` importa `chaskicode.dev/saas/tests/...` (helpers y fixtures que nunca van en el binario) | `api/tests/arch/testsimports_test.go` | `…/testdata/arch` (también un fichero con la etiqueta `integration`) |
| `[rls-vars-only-in-db]`: ningún fichero Go fuera de `core/kit/db` y de `api/tests/db` llama a `set_config` ni nombra las variables de RLS | `api/tests/arch/rlsvars_test.go` | `…/testdata/rlsvars` |
| `[contract-platform-owned]`: `app.yaml` no define seguridad raíz, `servers` ni `securitySchemes`, ni la cabecera de tenant; usa la misma versión de OpenAPI; sus `$ref` van solo a componentes, propios (`#/components/…`) o de plataforma (`core.yaml#/components/…`), y resuelven; ningún path item es un `$ref` | `api/tools/specmerge` (en `mise run generate`) | `api/tools/specmerge/main_test.go` |
| `[contract-duplicate]`: `app.yaml` no repite una ruta (aunque cambien los nombres de parámetro), un `operationId` ni un nombre de componente (comparados sin mayúsculas, `_` ni `-`, como los ven los generadores), ni un tag de plataforma | `api/tools/specmerge` | `api/tools/specmerge/main_test.go` |
| `[contract-module-key]`: cada operación tiene un solo tag, declarado y con módulo en su zona (`core/<m>` o `app/modules/<m>`); cada `codegen.yaml` tiene `include-tags: [<m>]`, `package: <m>api` y `output: gen/openapi/<m>/<m>.gen.go`, y alguna operación con su tag | `api/tools/specmerge` | `api/tools/specmerge/main_test.go` |
| `[platform-imports-app]` (front): `web/src/core` no importa `web/src/app` salvo en `core/app-registry.ts` | `web/src/core/__tests__/architecture.test.ts` (Vitest, `mise run web:test`) | El propio test, con cada forma de importar |
| `[feature-boundaries]` (front): un fichero de `web/src/app/features/<a>/` no importa nada de `web/src/app/features/<b>/`; lo compartido va en `web/src/app/shared/` | `web/src/core/__tests__/architecture.test.ts` (Vitest, `mise run web:test`) | El propio test, con cada forma de importar |

Los fixtures son mini-módulos con el mismo module path que `api/`, para que las reglas les
apliquen. `TestLintRules` ejecuta golangci-lint con `api/.golangci.yml` sobre cada uno y comprueba
que falla con el mensaje esperado y que los casos permitidos no se marcan. Si cambias la
configuración del lint y una regla deja de funcionar, ese test falla.

Los tests de arquitectura llevan el build tag `arch`: `go test ./...` no los ejecuta;
`mise run arch` sí. Listan los paquetes con `-tags integration`, y golangci-lint usa las dos
etiquetas (`build-tags` en `api/.golangci.yml`), para que las reglas también vean los
ficheros de los tests de integración. Un fichero detrás de otra etiqueta escaparía a las
reglas: una etiqueta nueva se añade también a `listPackages` y a `build-tags`.

## Tests unitarios y de integración

Los tests que necesitan Docker (Testcontainers) llevan `//go:build integration`:
`mise run test` compila sin ellos y termina sin Docker; `mise run test:integration` los
incluye (y repite los unitarios, porque Go no puede dejar fuera los ficheros sin etiqueta).
Viven en `api/tests/` (`db/`, `integration/<módulo>/`, con el helper `testkit/`), salvo los
que usan código no exportado, son de `package main` o importan el interior de su módulo.

## Qué hacer cuando falla

- **lint / arch**: el mensaje empieza por el nombre de la regla y dice cómo arreglarlo. Se corrige
  el código; un `//nolint` solo con un comentario que explique por qué.
- **generate**: ejecuta `mise run generate` y commitea `api/gen` y `web/src/gen`.
- **web** con `routeTree.gen.ts`: ejecuta `npm run build` en `web/` y commitea `web/src/gen/routeTree.gen.ts`.
  En local, `generate:check` también falla si has regenerado pero aún no has hecho `git add`.
- **security**:
  - govulncheck: actualiza el módulo afectado (`go get <módulo>@<versión corregida>`).
  - gitleaks: si es un secreto real, rótalo primero; luego reescribe el historial o, si es
    un falso positivo, añádelo a `.gitleaksignore` con su fingerprint y un comentario.
  - trivy: actualiza la dependencia o la imagen base. Ignorar un CVE (`.trivyignore`) exige un
    comentario con el motivo y una fecha de revisión.
- **e2e**: descarga el artefacto `e2e-screenshots-and-traces` y abre la traza del test con
  `npx playwright show-trace <zip>` (en `web/`), o `e2e-api-log` para ver qué respondió la
  API. Para reproducirlo en local: `mise run up && mise run migrate`, `mise run dev` y
  `mise run web:e2e`.
- **test** solo en CI: suele ser Docker/Testcontainers. Revisa el log del contenedor en la salida
  del test.

## Proteger `main` (manual, en GitHub)

`ci-ok` aparece como check seleccionable después de que el workflow se haya ejecutado al menos
una vez (abre un PR para provocarlo).

1. **Settings → Rules → Rulesets → New ruleset → New branch ruleset**.
2. Nombre: `main`. **Enforcement status**: *Active*.
3. **Target branches → Add target → Include default branch**.
4. Marca:
   - **Restrict deletions**.
   - **Require a pull request before merging** (aprobaciones requeridas: 0 si trabajas solo;
     marca *Require review from Code Owners* cuando haya más gente).
   - **Require status checks to pass** → *Add checks* → `ci-ok`. Marca también
     *Require branches to be up to date before merging*.
   - **Block force pushes**.
5. Deja **Bypass list** vacía para que nadie, ni el propietario, pueda hacer push directo a `main`.
6. **Create**.

Como el único check obligatorio es `ci-ok`, añadir o renombrar jobs no exige tocar la regla: basta
con añadirlos a `needs` de `ci-ok`. Si el job nuevo es pesado y no tiene sentido para un cambio de
documentación, lleva además `needs: changes` e `if: needs.changes.outputs.code == 'true'`.

## Dependencias

Renovate (`renovate.json`) abre PRs los lunes antes de las 6:00, en grupos separados:
- módulos Go;
- paquetes npm;
- GitHub Actions (fijadas por digest);
- herramientas de `mise.toml`, junto con la versión de mise que usa el CI.

La versión de Go de `mise.toml` y la imagen `golang` del Dockerfile van en el mismo PR. No hay
fusión automática: cada PR pasa por el CI como cualquier otro cambio.

Hay que instalar la GitHub App de Renovate en el repositorio para que empiece a funcionar.
