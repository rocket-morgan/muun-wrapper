# muun-wrapper

Servicio headless de una wallet Muun para una sola seed y un solo usuario. Usa el core Go de
Apollo fijado al commit `e5f5e1bb54f611ef457c56207f2a34ac85b00827` y se comunica con Houston.

La fase actual crea una wallet nueva, persiste la clave base cifrada en `/data/wallet.json`,
expone direcciones onchain Taproot Muun, genera invoices Lightning BOLT11, consulta el saldo
gastable unificado y envía pagos onchain o Lightning. El directorio de datos contiene el JWT de
Houston, la clave privada cifrada y la base SQLite de secretos Lightning; debe conservarse junto
con la passphrase configurada.

## Inicio

```bash
cp .env.example compose.secrets
# Reemplazá ambos valores por secretos largos y aleatorios.
./scripts/up.sh
```

`LOCAL_UID` y `LOCAL_GID` tienen que coincidir con el propietario de `./data`. Compose usa
`1000` por defecto; si tu usuario tiene otro UID/GID, iniciá con
`LOCAL_UID=$(id -u) LOCAL_GID=$(id -g) ./scripts/up.sh`.

La API escucha únicamente en `127.0.0.1:18080` por el mapeo de Compose. Todos los endpoints de
wallet requieren el token estático. Para usar `curl` desde el host, cargá el mismo token que
configuraste en `compose.secrets` sin dejarlo en el historial:

```bash
read -r -s -p "API token: " MUUN_WRAPPER_API_TOKEN; echo
export MUUN_WRAPPER_API_TOKEN
```

Después ejecutá los endpoints:

```bash
curl -X POST http://127.0.0.1:18080/wallet/create \
  -H "Authorization: Bearer $MUUN_WRAPPER_API_TOKEN"

curl http://127.0.0.1:18080/receive/address \
  -H "Authorization: Bearer $MUUN_WRAPPER_API_TOKEN"

curl http://127.0.0.1:18080/balance \
  -H "Authorization: Bearer $MUUN_WRAPPER_API_TOKEN"

curl http://127.0.0.1:18080/operations \
  -H "Authorization: Bearer $MUUN_WRAPPER_API_TOKEN"

curl -X POST http://127.0.0.1:18080/receive/invoice \
  -H "Authorization: Bearer $MUUN_WRAPPER_API_TOKEN" \
  -H "Content-Type: application/json" \
  --data '{"amountSatoshis":2500}'

curl -X POST http://127.0.0.1:18080/sync \
  -H "Authorization: Bearer $MUUN_WRAPPER_API_TOKEN"

curl http://127.0.0.1:18080/invoices/<paymentHash> \
  -H "Authorization: Bearer $MUUN_WRAPPER_API_TOKEN"

curl -X POST http://127.0.0.1:18080/send \
  -H "Authorization: Bearer $MUUN_WRAPPER_API_TOKEN" \
  -H "Content-Type: application/json" \
  --data '{"destination":"bc1qexample","amountSatoshis":10000,"description":"onchain"}'

curl -X POST http://127.0.0.1:18080/send/preview \
  -H "Authorization: Bearer $MUUN_WRAPPER_API_TOKEN" \
  -H "Content-Type: application/json" \
  --data '{"amountSatoshis":2500,"method":"onchain"}'

curl -X POST http://127.0.0.1:18080/send \
  -H "Authorization: Bearer $MUUN_WRAPPER_API_TOKEN" \
  -H "Content-Type: application/json" \
  --data '{"destination":"lnbc...","amountSatoshis":2500,"idempotencyKey":"withdrawal-2026-09-28-1"}'

curl http://127.0.0.1:18080/send/status/withdrawal-2026-09-28-1 \
  -H "Authorization: Bearer $MUUN_WRAPPER_API_TOKEN"
```

`POST /wallet/create` sólo puede ejecutarse una vez por directorio de datos. Devuelve la primera
dirección onchain y reserva su índice en Houston. `GET /receive/address` reserva una dirección
nueva. `GET /balance` devuelve el saldo gastable unificado en satoshis calculado por Houston:
el último UTXO de su progresión menos la deuda esperada. `GET /healthz` no requiere autenticación.
`GET /operations` devuelve el historial actual con dirección, estado, monto, fee, hash y
confirmaciones cuando existen. `POST /receive/invoice` recibe `amountSatoshis`, registra los
secretos necesarios con Houston y devuelve una invoice Lightning BOLT11 por ese monto.
`POST /receive/invoice` devuelve además el `paymentHash` de la invoice.
`GET /invoices/{paymentHash}` informa si esa invoice ya fue pagada. Responde sólo por invoices que
emitió esta wallet: un payment hash desconocido devuelve `404` en vez de un "no pagado"
indefinido. El campo `state` vale `pending` (sin pago reportado), `receiving` (el pago llegó pero
su incoming swap no está liquidado, así que no hay preimage y los fondos no son gastables) o
`settled` (el preimage prueba el pago). Es idempotente: no dispara sincronización. `GET /operations`
incluye `paymentHash` y `preimage` en los incoming swaps, para correlacionar operaciones con
invoices.
`POST /sync` busca incoming swaps pendientes en la historia de Houston y los liquida; ejecutalo
después de pagar una invoice si no querés esperar al poller. El servidor también sincroniza al
iniciar y cada `MUUN_WRAPPER_SYNC_INTERVAL` (20 segundos por defecto). `POST /send` recibe una dirección Bitcoin o una invoice Lightning BOLT11 con monto fijo, más
`amountSatoshis`. En onchain firma y publica la transacción. En Lightning crea un submarine swap
v2, lo valida contra la invoice y publica la transacción de funding; si Houston presta el monto,
no hay transacción onchain. `POST /send/preview` calcula la comisión sin firmar ni publicar. En
onchain puede omitirse `destination`; el wrapper usa un output Taproot sintético porque el cálculo
de comisión depende de los UTXOs y del monto. Para retiros automatizados enviá `idempotencyKey` en
`POST /send`: se persiste en `/data/send-idempotency.json`, una respuesta completada se repite con
`200` e `Idempotent-Replay: true`, y una key `in_flight` debe consultarse con
`GET /send/status/{key}` o reconciliarse con `POST /send/reconcile/{key}` antes de decidir un
reintento. No se reintenta automáticamente un error `502 submit_failed`, porque Houston puede
haber aceptado la operación.

## Emergency Kit

La recovery code no se guarda en la wallet ni se devuelve por la API. Generala una vez, guardala
fuera del directorio de datos y usala desde entrada estándar para registrarla. El kit resultante
contiene material cifrado por esa code; guardalo con permisos restrictivos junto con la code, pero
en ubicaciones separadas.

```bash
mkdir -p secrets
chmod 700 secrets

docker compose exec -T muun-wrapper muun-wrapper recovery-code \
  > secrets/recovery-code.txt
chmod 600 secrets/recovery-code.txt

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper recovery-setup --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN" --recovery-code-file /dev/stdin' \
  < secrets/recovery-code.txt

umask 077
docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper emergency-kit --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN"' \
  > secrets/emergency-kit.json
```

`POST /recovery/setup` registra la code con Houston y crea el material de recuperación.
`GET /recovery/kit` devuelve el export como JSON, con sus claves cifradas, HTML imprimible y
metadata para PDF. La code nunca forma parte de esa respuesta.

El mismo binario incluye los comandos cliente:

```bash
docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper create --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN"'

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper address --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN"'

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper balance --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN"'

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper operations --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN"'

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper invoice --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN" --amount-sats 2500'

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper sync --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN"'

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper send --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN" --destination bc1qexample --amount-sats 10000 --description onchain'

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper send-preview --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN" --amount-sats 2500 --method onchain'

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper send --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN" --destination lnbc... --amount-sats 2500 --idempotency-key withdrawal-2026-09-28-1'
```

## Desarrollo y verificación

```bash
make test
make build
```

El builder clona Apollo dentro de Docker, compila `librs` para Linux y aplica un `replace`
temporal hacia `/deps/apollo/libwallet`; no depende de una copia local de Apollo para construir
la imagen.

## Estado de las operaciones

El saldo se consulta desde la progresión de UTXOs de Houston. `POST /send` acepta direcciones
onchain y invoices Lightning con monto fijo. Las invoices sin monto quedan fuera de esta fase.

## Documentación de referencia

La especificación de la API REST, compatible con Swagger UI y otras herramientas OpenAPI, está en
[`docs/openapi.json`](docs/openapi.json). La referencia de comandos cliente está en
[`docs/cli.md`](docs/cli.md). Para operar el stack local usá `./scripts/up.sh` y
`./scripts/down.sh`.
