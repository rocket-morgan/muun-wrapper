# CLI

El binario `muun-wrapper` tiene un modo servidor y un cliente HTTP. El servicio de Compose ya
ejecuta `server`; los demás comandos consumen la API protegida por el token estático.

```bash
muun-wrapper <server|create|address|balance|operations|invoice|send|sync>
```

Los comandos cliente aceptan estas opciones:

| Opción | Variable equivalente | Descripción |
| --- | --- | --- |
| `--api-url URL` | `MUUN_WRAPPER_API_URL` | URL base de la API. Por defecto `http://127.0.0.1:8080`. |
| `--api-token TOKEN` | `MUUN_WRAPPER_API_TOKEN` | Token bearer requerido por la API. |

Al usar Compose, ejecutá el cliente dentro del contenedor para que use el token configurado sin
copiarlo al historial del shell:

```bash
docker compose exec muun-wrapper muun-wrapper balance \
  --api-url http://127.0.0.1:8080 \
  --api-token "$MUUN_WRAPPER_API_TOKEN"
```

## Comandos

| Comando | Efecto | Opciones requeridas |
| --- | --- | --- |
| `create` | Crea la wallet para el directorio de datos actual y devuelve la primera dirección. Sólo funciona una vez. | — |
| `address` | Reserva y devuelve una nueva dirección onchain Taproot. | — |
| `balance` | Devuelve el saldo gastable en satoshis. | — |
| `operations` | Lista las operaciones informadas por Houston. | — |
| `invoice` | Crea una invoice BOLT11 para recibir Lightning. | `--amount-sats N` |
| `send` | Envía a una dirección Bitcoin o una invoice BOLT11 con monto fijo. | `--destination DESTINO`, `--amount-sats N` |
| `sync` | Liquida los incoming swaps Lightning pendientes. | — |

`send` acepta `--description TEXTO` de forma opcional. La comisión incluida en su salida es la
fee de red; el historial puede mostrar además la deuda pendiente de swaps que Houston liquida al
gastar UTXOs.

## Ejemplos

```bash
docker compose exec muun-wrapper muun-wrapper address \
  --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN"

docker compose exec muun-wrapper muun-wrapper invoice \
  --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN" \
  --amount-sats 1000

docker compose exec muun-wrapper muun-wrapper send \
  --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN" \
  --destination lnbc... --amount-sats 1000 --description "prueba Lightning"

docker compose exec muun-wrapper muun-wrapper sync \
  --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN"
```

## Servidor

`muun-wrapper server` necesita `MUUN_WRAPPER_PASSPHRASE` y `MUUN_WRAPPER_API_TOKEN`. También
reconoce `MUUN_WRAPPER_DATA_DIR`, `MUUN_WRAPPER_HOUSTON_URL`,
`MUUN_WRAPPER_PRIMARY_CURRENCY`, `MUUN_WRAPPER_LISTEN_ADDR` y
`MUUN_WRAPPER_SYNC_INTERVAL`. Compose monta `./data` como `/data` y publica el puerto sólo en
`127.0.0.1:18080`.
