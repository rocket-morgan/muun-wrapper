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
cp .env.example .env
# Reemplazá ambos valores por secretos largos y aleatorios.
docker compose up --build -d
```

`LOCAL_UID` y `LOCAL_GID` tienen que coincidir con el propietario de `./data`; por defecto son
`1000`, que corresponde al entorno de desarrollo actual.

La API escucha únicamente en `127.0.0.1:18080` por el mapeo de Compose. Todos los endpoints de
wallet requieren el token estático:

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

curl -X POST http://127.0.0.1:18080/send \
  -H "Authorization: Bearer $MUUN_WRAPPER_API_TOKEN" \
  -H "Content-Type: application/json" \
  --data '{"destination":"bc1qexample","amountSatoshis":10000,"description":"onchain"}'

curl -X POST http://127.0.0.1:18080/send \
  -H "Authorization: Bearer $MUUN_WRAPPER_API_TOKEN" \
  -H "Content-Type: application/json" \
  --data '{"destination":"lnbc...","amountSatoshis":2500}'
```

`POST /wallet/create` sólo puede ejecutarse una vez por directorio de datos. Devuelve la primera
dirección onchain y reserva su índice en Houston. `GET /receive/address` reserva una dirección
nueva. `GET /balance` devuelve el saldo gastable unificado en satoshis calculado por Houston:
el último UTXO de su progresión menos la deuda esperada. `GET /healthz` no requiere autenticación.
`GET /operations` devuelve el historial actual con dirección, estado, monto, fee, hash y
confirmaciones cuando existen. `POST /receive/invoice` recibe `amountSatoshis`, registra los
secretos necesarios con Houston y devuelve una invoice Lightning BOLT11 por ese monto.
`POST /sync` busca incoming swaps pendientes en la historia de Houston y los liquida; ejecutalo
después de pagar una invoice si no querés esperar al poller. El servidor también sincroniza al
iniciar y cada `MUUN_WRAPPER_SYNC_INTERVAL` (20 segundos por defecto). `POST /send` recibe una
dirección Bitcoin o una invoice Lightning BOLT11 con monto fijo, más `amountSatoshis`. En onchain
firma y publica la transacción. En Lightning crea un submarine swap v2, lo valida contra la invoice
y publica la transacción de funding; si Houston presta el monto, no hay transacción onchain.

El mismo binario incluye los comandos cliente:

```bash
docker compose exec muun-wrapper muun-wrapper create \
  --api-url http://127.0.0.1:18080 \
  --api-token "$MUUN_WRAPPER_API_TOKEN"

docker compose exec muun-wrapper muun-wrapper address \
  --api-url http://127.0.0.1:18080 \
  --api-token "$MUUN_WRAPPER_API_TOKEN"

docker compose exec muun-wrapper muun-wrapper balance \
  --api-url http://127.0.0.1:18080 \
  --api-token "$MUUN_WRAPPER_API_TOKEN"

docker compose exec muun-wrapper muun-wrapper operations \
  --api-url http://127.0.0.1:18080 \
  --api-token "$MUUN_WRAPPER_API_TOKEN"

docker compose exec muun-wrapper muun-wrapper invoice \
  --api-url http://127.0.0.1:18080 \
  --api-token "$MUUN_WRAPPER_API_TOKEN" \
  --amount-sats 2500

docker compose exec muun-wrapper muun-wrapper sync \
  --api-url http://127.0.0.1:18080 \
  --api-token "$MUUN_WRAPPER_API_TOKEN"

docker compose exec muun-wrapper muun-wrapper send \
  --api-url http://127.0.0.1:18080 \
  --api-token "$MUUN_WRAPPER_API_TOKEN" \
  --destination bc1qexample \
  --amount-sats 10000 \
  --description onchain

docker compose exec muun-wrapper muun-wrapper send \
  --api-url http://127.0.0.1:18080 \
  --api-token "$MUUN_WRAPPER_API_TOKEN" \
  --destination lnbc... \
  --amount-sats 2500
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
