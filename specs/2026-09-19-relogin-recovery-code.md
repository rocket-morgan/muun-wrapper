# Plan: sesión persistente con Houston y relogin asistido

## Evidencia obtenida el 19 de septiembre de 2026

- El `sessionToken` persistido en la wallet creada el 16 de septiembre de 2026 siguió aceptado por Houston el 19 de septiembre.
- Dos `GET /operations` consecutivos contra `https://pub.api.muun.io/houston/` respondieron `200 OK` sin enviar un Bearer nuevo en `Authorization`.
- El token es un JWT firmado con `HS512`, pero su payload solo contiene `sub`; no contiene `iat`, `nbf` ni `exp`. No se puede estimar su vencimiento desde el cliente.
- Apollo adjunta el Bearer actual a cada request y persiste otro Bearer si Houston lo incluye en una respuesta. No llama a un endpoint de refresh.
- `POST sessions-v2/first` es el flujo de signup: Apollo crea una nueva base private key y un usuario. No se debe reutilizar para recuperar una sesión de una wallet existente.

## Objetivo

Mantener la sesión actual mientras Houston la acepte y obtener evidencia operativa suficiente antes de agregar un relogin. Recovery code será un mecanismo asistido de recuperación, no la renovación automática predeterminada.

## Implementación propuesta

1. Instrumentar de forma segura las requests a Houston:
   - Registrar solamente status HTTP, presencia de `Authorization` en la respuesta y un fingerprint no reversible del token.
   - No registrar tokens, bodies de operaciones, keys ni recovery codes.
   - Registrar la primera aparición de un token distinto y el primer `401 Unauthorized`.

2. Implementar compatibilidad con rotación de Bearer:
   - Si Houston devuelve `Authorization: Bearer <token>` en una respuesta exitosa, persistirlo atómicamente en `wallet.json`.
   - Mantener el token anterior si la respuesta no trae `Authorization`.
   - Serializar la actualización para que una operación concurrente no sobrescriba un token reciente con una copia vieja del record.

3. Definir la política de sesión vencida después de observar un `401` real:
   - No reintentar `sessions-v2/first`.
   - Devolver un error explícito de sesión no autorizada mientras no exista un flujo normal de relogin confirmado.
   - Confirmar con Houston o una prueba controlada qué endpoint permite renovar una sesión para una wallet existente.

4. Evaluar recovery code solo como fallback asistido:
   - Apollo soporta `sessions-v2/recovery-code/start` y `sessions-v2/recovery-code/finish`, firmando un challenge con la key derivada del recovery code.
   - Este flujo puede requerir autorización por email y no debe ejecutarse automáticamente.
   - Si se incorpora, exponerlo como una operación manual de recuperación separada del loop normal de sesión.

## Pruebas y aceptación

- Un fake Houston que rota el Bearer confirma que el siguiente request y un reinicio del proceso usan el token nuevo.
- Un fake Houston sin header `Authorization` confirma que el token persistido no cambia.
- Un `401` se informa claramente y no llama a `sessions-v2/first`.
- La instrumentación no emite secretos ni datos de wallet.
- Ejecutar `docker build -f Dockerfile.test -t muun-wrapper-test .` y `make docs`.
