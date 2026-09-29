# CLI

El binario `muun-wrapper` tiene un modo servidor y un cliente HTTP. El servicio de Compose ya
ejecuta `server`; los demás comandos consumen la API protegida por el token estático.

```bash
muun-wrapper <server|create|address|balance|operations|operation|invoice|invoice-status|send-preview|send|send-status|send-reconcile|sync|recovery-code|recovery-setup|emergency-kit>
```

Los comandos cliente aceptan estas opciones:

| Opción | Variable equivalente | Descripción |
| --- | --- | --- |
| `--api-url URL` | `MUUN_WRAPPER_API_URL` | URL base de la API. Por defecto `http://127.0.0.1:8080`. |
| `--api-token TOKEN` | `MUUN_WRAPPER_API_TOKEN` | Token bearer requerido por la API. |

Al usar Compose, ejecutá el cliente dentro del contenedor para que use el token configurado sin
copiarlo al historial del shell:

```bash
docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper balance --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN"'
```

## Comandos

| Comando | Efecto | Opciones requeridas |
| --- | --- | --- |
| `create` | Crea la wallet para el directorio de datos actual y devuelve la primera dirección. Sólo funciona una vez. | — |
| `address` | Reserva y devuelve una nueva dirección onchain Taproot. | — |
| `balance` | Devuelve el saldo gastable en satoshis. | — |
| `operations` | Lista operaciones con `status`, `paymentState`, `createdAt`, `transactionId` nullable y confirmaciones. | — |
| `operation` | Consulta una operación con el mismo shape normalizado del listado. | `--operation-id N` |
| `invoice` | Crea una invoice BOLT11 para recibir Lightning. Devuelve también su `paymentHash`. | `--amount-sats N` |
| `invoice-status` | Informa si una invoice emitida ya fue pagada. | `--payment-hash HEX` |
| `send-preview` | Cotiza un envío sin firmarlo ni publicarlo. En onchain no necesita destino. | `--amount-sats N`; `--method onchain|lightning` opcional |
| `send` | Envía a una dirección Bitcoin o una invoice BOLT11 con monto fijo. | `--destination DESTINO`, `--amount-sats N`; `--idempotency-key KEY` opcional |
| `send-status` | Consulta el estado local de una key persistente. | `--idempotency-key KEY` |
| `send-reconcile` | Busca una key `in_flight` en el historial de Houston. | `--idempotency-key KEY` |
| `sync` | Liquida los incoming swaps Lightning pendientes. | — |
| `recovery-code` | Genera una recovery code nueva. No usa la API ni la persiste. | — |
| `recovery-setup` | Registra una recovery code y prepara el emergency kit. | `--recovery-code-file ARCHIVO` |
| `emergency-kit` | Imprime el export JSON del emergency kit ya configurado. | — |

`invoice-status` responde sólo por invoices que emitió esta wallet: un payment hash desconocido
devuelve un error en vez de un "no pagado" indefinido. Devuelve tres estados:

| `state` | Significado |
| --- | --- |
| `pending` | La invoice es de esta wallet y Houston todavía no reporta el pago. |
| `receiving` | El pago llegó pero su incoming swap no está liquidado: no hay preimage y los fondos no son gastables. El poller lo liquida cada `MUUN_WRAPPER_SYNC_INTERVAL`, o forzalo con `sync`. |
| `settled` | El swap está liquidado y el preimage prueba el pago. |

Comparar `receivedSatoshis` con `amountSatoshis` permite detectar un monto distinto al pedido.

`send` acepta `--description TEXTO` y `--idempotency-key KEY` de forma opcional. Una misma key
sólo puede reutilizarse con el mismo destino y monto. Si el servicio responde `502 submit_failed`,
no reenvíes el retiro: consultá `send-status` y usá `send-reconcile` para buscar el `requestId`
determinístico en Houston. La comisión incluida en la salida es la fee de red; el historial puede
mostrar además la deuda pendiente de swaps que Houston liquida al gastar UTXOs.

`send-preview --method onchain` permite calcular la comisión sin `--destination`; el wrapper usa
un output P2TR sintético. `send-preview --method lightning` requiere una BOLT11 con monto fijo
igual a `--amount-sats`.

`send-status` usa una key persistente. Una key inexistente responde HTTP 404 con
`code: idempotency_key_not_found`; ese código es el capability check que el backend usa antes de
habilitar retiros idempotentes.

`operations` y `operation` mantienen el `status` crudo de Houston y devuelven también
`paymentState`, `createdAt` RFC3339 UTC, `transactionId` (string onchain o `null` Lightning) y
`confirmations`. `receiverAddress` aparece cuando Houston la reporta, incluidos depósitos onchain.

## Ejemplos

```bash
docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper address --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN"'

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper invoice --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN" --amount-sats 1000'

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper send-preview --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN" --amount-sats 1000 --method onchain'

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper send --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN" --destination lnbc... --amount-sats 1000 --description "prueba Lightning" --idempotency-key retiro-1000-1'

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper send-status --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN" --idempotency-key retiro-1000-1'

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper sync --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN"'

docker compose exec -T muun-wrapper sh -c \
  'muun-wrapper invoice-status --api-url http://127.0.0.1:8080 --api-token "$MUUN_WRAPPER_API_TOKEN" --payment-hash 8f3a...'
```

## Recovery code y emergency kit

La code da acceso a las claves cifradas del kit: guardala offline y no la agregues a la shell, a
un repositorio ni al directorio `data`. El CLI no la persiste. `recovery-setup` la lee de un
archivo, por lo que puede recibirla desde `/dev/stdin` dentro del contenedor.

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

El JSON incluye las dos claves cifradas, fingerprints, checksum, HTML imprimible y metadata para
PDF. Conservá ese archivo separado de la recovery code.

## Servidor

`muun-wrapper server` necesita `MUUN_WRAPPER_PASSPHRASE` y `MUUN_WRAPPER_API_TOKEN`. También
reconoce `MUUN_WRAPPER_DATA_DIR`, `MUUN_WRAPPER_HOUSTON_URL`,
`MUUN_WRAPPER_PRIMARY_CURRENCY`, `MUUN_WRAPPER_LISTEN_ADDR` y
`MUUN_WRAPPER_SYNC_INTERVAL`. Compose monta `./data` como `/data` y publica el puerto sólo en
`127.0.0.1:18080`.
