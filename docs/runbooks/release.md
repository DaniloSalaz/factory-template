# Runbook: imagen de la API

`.github/workflows/release.yml` publica la imagen cuando un push a `main` pasa el CI
(`ci-ok` en verde). Los pull requests no publican nada.

## Qué publica

- `ghcr.io/<owner>/factory-template-api`, para `linux/amd64` y `linux/arm64`, construida
  con buildx desde el commit que comprobó el CI.
- Etiquetas: el SHA completo del commit (la que despliega un entorno: inmutable) y `main`
  (la última publicada; solo para mirar).
- El binario conoce su versión (el SHA, `-X main.buildVersion`): sale en cada línea del log
  (`version`) y en `GET /healthz` (`{"status":"ok","version":"<sha>"}`).
- Atestaciones: procedencia (`mode=max`) y SBOM de BuildKit, guardadas con la imagen, y una
  atestación de GitHub del digest. Para verificarla:

  ```sh
  gh attestation verify oci://ghcr.io/<owner>/factory-template-api:<sha> --owner <owner>
  docker buildx imagetools inspect ghcr.io/<owner>/factory-template-api:<sha> \
    --format '{{ json .SBOM }}' | head
  ```

- Trivy escanea la imagen ya publicada (por digest, en las dos plataformas) y el job falla
  con vulnerabilidades HIGH o CRITICAL que tengan corrección. La imagen ya está en el
  registro: **no despliegues un SHA cuyo job `image` haya fallado**. Arréglalo (actualiza la
  imagen base o la dependencia) y el siguiente commit en `main` publica otra.

El paquete de GHCR nace privado. Para que un servidor lo descargue, dale acceso o un token
con `read:packages` (S2).

## Desplegar una versión (orden)

1. `api migrate` con la imagen **nueva**, antes de cambiar de versión. Solo necesita
   `APP_ENV` y `MIGRATIONS_DATABASE_URL` (`app_owner`); no arranca el servidor HTTP y sale
   con código distinto de 0 si algo falla. Si falla, no sigas.

   ```sh
   docker run --rm -e APP_ENV=production -e MIGRATIONS_DATABASE_URL=… \
     ghcr.io/<owner>/factory-template-api:<sha> migrate
   ```

2. Arranca la versión nueva con su configuración (`APP_ENV`, https, SMTP…: si falta algo, no
   arranca y el log dice qué variable) y comprueba `GET /healthz` (la versión) y
   `GET /readyz`.

Las migraciones deben ser compatibles con la versión anterior mientras conviven (añadir
antes de usar, quitar después de dejar de usar): `api migrate` corre mientras la versión
vieja sigue sirviendo.

## En local

```sh
mise run docker:build    # factory-template-api:ci, con VERSION = el commit actual
mise run docker:scan     # Trivy, como el job docker del CI
```
