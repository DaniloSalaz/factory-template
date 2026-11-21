# Runbook: CI

El CI (`.github/workflows/ci.yml`) ejecuta las mismas tareas de mise que `mise run ci` en
local, con las versiones fijadas en `mise.toml`. Si algo pasa en local y falla en CI (o al
revés), lo primero es comprobar que `mise install` está al día.

Se ejecuta en cada `pull_request` y en cada `push` a `main`. Las ejecuciones antiguas del
mismo PR se cancelan; las de `main` siempre terminan.

## Jobs

| Job | Qué comprueba | En local |
|---|---|---|
| `lint` | golangci-lint (incluye las reglas de arquitectura de depguard y forbidigo), actionlint + shellcheck sobre los workflows y los tests de arquitectura | `mise run lint:go`, `mise run lint:actions`, `mise run arch` |
| `test` | `go test -race -cover ./...`; la integración de `/readyz` arranca Postgres con Testcontainers en el Docker del runner | `mise run test` (Docker arrancado) |
| `generate` | Regenera sqlc, oapi-codegen, orval y la seguridad por ruta del front (`web/src/gen/routeSecurity.ts`) y falla si cambia algo en `api/gen` o `web/src/gen` | `mise run generate:check` |
| `security` | govulncheck, gitleaks sobre todo el historial y `trivy fs` (dependencias y Dockerfile), fallando con HIGH o CRITICAL | `mise run security` |
| `web` | `npm ci`, Biome, `npm run build` (genera `web/src/routeTree.gen.ts` con la CLI de TanStack Router, luego `tsc -b` y `vite build`; falla si el árbol no coincide con el commit) y los tests unitarios con Vitest, incluido el de arquitectura del front | `mise run web:check`, `mise run web:test` |
| `e2e` | El recorrido de Playwright (`web/e2e`) contra la API real: Postgres 18 y Mailpit (el SMTP de desarrollo de `mise.toml`) como servicios, el bootstrap de `infra/db`, `mise run migrate`, la API compilada y arrancada con una `WORKOS_API_KEY` ficticia, y Vite (lo arranca Playwright). Sube las capturas (`web/e2e-screenshots/`) y las trazas (`web/test-results/`, todas en CI) como artefacto `e2e-screenshots-and-traces`; si falla, también el log de la API (`e2e-api-log`) | `mise run up && mise run migrate`, `mise run dev` y `mise run web:e2e` |
| `docker` | Construye la imagen de la API (sin publicarla) y la escanea con Trivy | `mise run docker:scan` |
| `release` (otro workflow) | Tras un push a `main` con el CI en verde: imagen multiplataforma en GHCR, atestaciones y Trivy de la imagen publicada (`docs/runbooks/release.md`) | `mise run docker:build`, `mise run docker:scan` |
| `ci-ok` | Depende de todos los anteriores y falla si alguno no terminó en `success` (también si se saltó o se canceló) | — |

El job `e2e` no llama nunca a WorkOS: el arranque de la API solo comprueba que
`WORKOS_API_KEY` existe (la usa para derivar la clave que sella los intentos de login y en
las llamadas de login y MFA), las sesiones se siembran por SQL (`web/e2e/db.ts`, que en CI
entra en el contenedor de servicio con `docker exec`, `E2E_PG_CONTAINER`) y las pantallas de
MFA usan respuestas simuladas. Si un test nuevo necesitara WorkOS, no cabe en este job.

`mise run ci` ejecuta en orden: `lint:go`, `lint:actions`, `arch`, `test`, `generate:check`,
`security:govulncheck`, `security:gitleaks`, `web:check` y `web:test`. El job `e2e` no está
en `ci` porque necesita Postgres y la API en marcha. Los escaneos de Trivy (`security:trivy`
y `docker:scan`) no están en `ci` porque descargan su base de datos y tardan más; ejecútalos con
`mise run security` y `mise run docker:scan` cuando toques dependencias o el Dockerfile.

## Reglas de arquitectura

| Regla | Dónde se comprueba | Fixture que demuestra que funciona |
|---|---|---|
| `[env-only-in-config]`: `os.Getenv/LookupEnv/Environ` solo en `internal/platform/config` | forbidigo | `api/internal/archtest/testdata/lint/env-only-in-config` |
| `[platform-no-modules]`: `platform` no importa `modules` | depguard | `…/testdata/lint/platform-no-modules` |
| `[apperr-stdlib-only]`: `apperr` solo importa la stdlib | depguard | `…/testdata/lint/apperr-stdlib-only` |
| `[domain-pure]`: `domain` solo importa la stdlib y `platform/apperr` | depguard | `…/testdata/lint/domain-pure` |
| `[module-boundaries]`: fuera de `internal/modules/<x>` solo se importa su paquete raíz; `gen/openapi/<x>` y `gen/sqlc/<x>` son privados de `<x>` | `api/internal/archtest/boundaries_test.go` | `…/testdata/arch` |
| `[platform-imports-app]`: `web/src/platform` no importa `web/src/app` salvo en `platform/app-registry.ts` | `web/src/platform/architecture.test.ts` (Vitest, `mise run web:test`) | El propio test, con cada forma de importar |

Los fixtures son mini-módulos con el mismo module path que `api/`, para que las reglas les
apliquen. `TestLintRules` ejecuta golangci-lint con `api/.golangci.yml` sobre cada uno y comprueba
que falla con el mensaje esperado y que los casos permitidos no se marcan. Si cambias la
configuración del lint y una regla deja de funcionar, ese test falla.

Los tests de arquitectura llevan el build tag `arch`: `go test ./...` no los ejecuta;
`mise run arch` sí.

## Qué hacer cuando falla

- **lint / arch**: el mensaje empieza por el nombre de la regla y dice cómo arreglarlo. Se corrige
  el código; un `//nolint` solo con un comentario que explique por qué.
- **generate**: ejecuta `mise run generate` y commitea `api/gen` y `web/src/gen`.
- **web** con `routeTree.gen.ts`: ejecuta `npm run build` en `web/` y commitea `web/src/routeTree.gen.ts`.
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
con añadirlos a `needs` de `ci-ok`.

## Dependencias

Renovate (`renovate.json`) abre PRs los lunes antes de las 6:00, en grupos separados:
- módulos Go;
- paquetes npm;
- GitHub Actions (fijadas por digest);
- herramientas de `mise.toml`, junto con la versión de mise que usa el CI.

La versión de Go de `mise.toml` y la imagen `golang` del Dockerfile van en el mismo PR. No hay
fusión automática: cada PR pasa por el CI como cualquier otro cambio.

Hay que instalar la GitHub App de Renovate en el repositorio para que empiece a funcionar.
