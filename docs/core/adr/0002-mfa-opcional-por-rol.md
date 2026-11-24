# ADR 0002 — MFA opcional, exigida por rol desde el catálogo

- Estado: aceptado
- Fecha: 2026-11-20

## Contexto
El paso 4 exigía la MFA a todos los usuarios en el panel de WorkOS (*Required*) y la
deducía del método de login (`WORKOS_MFA_REQUIRED` más una lista blanca de métodos).
Queremos que la MFA sea voluntaria para todos y obligatoria solo para los roles que la
necesitan, y que cada app decida cuáles en su catálogo.

WorkOS no da ninguna señal por sesión de que hubo MFA: sus access tokens no traen `amr`,
`acr` ni nada equivalente, y sin *Required* ni siquiera documenta que AuthKit pida el
código a quien tiene un factor (diseño, sección 13). Deducirla de "el usuario tiene un
factor dado de alta" no prueba nada de la sesión.

## Decisión
- **La MFA la verifica la app después del login**, con la API de MFA de WorkOS: el
  backend crea un challenge del factor TOTP del usuario y verifica el código; solo
  `valid: true` cuenta. Es la vía que WorkOS documenta para una prueba ligada a la sesión.
- **Una sesión solo tiene MFA por `sessions.mfa_auth_time`**, y solo la fija la función
  `mark_session_mfa`, que crea una sesión nueva con otro token y revoca la anterior.
  `app_runtime` no tiene `UPDATE` sobre esa columna ni permisos en las tablas de MFA.
- **Challenges de un solo uso y con caducidad propia (5 min)**, ligados a la sesión;
  **límite de intentos** por usuario y por sesión (5 en 15 min); **antirreutilización**
  del último código aceptado por factor. WorkOS no documenta ninguna de las tres cosas.
- **El catálogo dice qué roles la exigen** y su ventana (owner: 12 h). Un catálogo sin
  roles con MFA es válido. Desaparecen `WORKOS_MFA_REQUIRED`, la lista blanca de métodos y
  la regla de arranque asociada.
- **Cambiar factores** (alta y baja) exige una MFA de menos de 10 minutos si el usuario
  ya tiene un factor verificado. El último factor no se puede quitar mientras un rol del
  usuario, en un tenant activo, la exija.
- En el panel de WorkOS, la MFA **no** está en *Required*.

## Consecuencias
- Vuelve un paso de verificación tras el login, que el diseño anterior había descartado.
  Ahora es posible porque no depende de que AuthKit informe de nada.
- Más superficie: dos tablas, siete funciones `SECURITY DEFINER` (una es un disparador) y
  cuatro endpoints. La foto de permisos de `app_runtime` solo gana `EXECUTE` en esas
  funciones.
- La primera alta es confianza en el primer uso: quien tenga la sesión de un usuario sin
  factor puede darle de alta uno. La cookie robada deja de valer al verificar.
- El límite de intentos es por réplica (memoria), como el resto (diseño, sección 6).
- La recuperación de un dispositivo perdido es manual (runbook) hasta el comando de
  administración del paso 7 (#18); no hay códigos de recuperación en esta versión (#19).
- Los usuarios SSO pueden usar la MFA de la app aunque la de WorkOS no les aplique.
