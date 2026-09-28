# Send preview e idempotencia persistente

Fecha: 2026-09-28

## Propósito

El backend consumidor necesita cotizar retiros y reintentar solicitudes HTTP sin crear dos pagos
cuando se pierde una respuesta. Esta fase agrega una preview sin firma ni broadcast y un registro
persistente por `idempotencyKey` para `POST /send`.

## Preview

`POST /send/preview` recibe `amountSatoshis`, `destination` opcional y `method` opcional.

- `onchain`: sin destino usa un output P2TR sintético. La comisión depende de `realtime`,
  `next-transaction-size` y el monto, no de la dirección receptora. No firma ni llama a
  `POST /operations`.
- `lightning`: exige una BOLT11 con monto fijo igual a `amountSatoshis`. Pide
  `operations/sswap/create`, valida el submarine swap y calcula la comisión con
  `analyzeLightningPayment`, pero no crea la operación ni publica una transacción.

La respuesta incluye `feeSatoshis`, `totalSatoshis` y, para Lightning, los componentes de routing
y sweep cuando Houston los proporciona.

## Idempotencia de send

El registro vive en `/data/send-idempotency.json`, separado de la SQLite interna de libwallet. Sus
escrituras usan un mutex del proceso, archivo temporal con permisos `0600`, `fsync`, `rename` y
`fsync` del directorio. No hay expiración.

Cada registro contiene la key, `sha256(destination|amountSatoshis)`, el estado, el resultado JSON,
un `request_id` UUIDv5 de namespace fijo y timestamps. El `request_id` se usa como `requestId` de
la operación saliente de Houston, de modo que un retry tras reiniciar conserva la misma identidad.

| Estado | Comportamiento de un nuevo `POST /send` con la misma key |
| --- | --- |
| `in_flight` | `409 send_in_progress`; no se vuelve a llamar a Houston. |
| `done` | `200`, mismo `SendResult` y header `Idempotent-Replay: true`. |
| `failed_pre_submit` | Puede reintentarse; vuelve a `in_flight`. |

Una key con otro destino o monto responde `422 idempotency_key_reused`.

## Punto de no retorno y errores

El wrapper escribe `in_flight` antes de su primer request a Houston. El punto de no retorno para
el contrato de retries es justo antes de `POST /operations`. Un error en la creación del submarine
swap sigue siendo pre-submit; un error al crear la operación, firmar o subir
`raw-transactions` puede corresponder a una operación ya aceptada.

| Momento | HTTP | Código | Estado de key |
| --- | --- | --- | --- |
| Parseo, invoice sin monto/vencida, saldo insuficiente, preview de swap o `sswap/create` falla | 400 | `validation` | `failed_pre_submit` |
| Houston responde 401/403 antes de `POST /operations` | 503 | `houston_session` | `failed_pre_submit` |
| La key ya está ejecutándose | 409 | `send_in_progress` | `in_flight` |
| La key cambia destino o monto | 422 | `idempotency_key_reused` | sin cambios |
| `POST /operations`, firma o `PUT raw-transactions` falla | 502 | `submit_failed` | `in_flight` |

`doJSON` conserva el status HTTP de Houston. En esta fase la detección de sesión vencida usa los
HTTP 401 y 403; no se observó un `errorCode` de sesión adicional en las pruebas locales.

## Reconciliación

`GET /send/status/{key}` lee sólo el registro local. `POST /send/reconcile/{key}` busca el
`requestId` persistido en `GET /operations` de Houston. Si encuentra una operación, guarda un
`SendResult` y cierra la key en `done`; si Houston devuelve al menos un `requestId` pero no el de
la key, pasa a `failed_pre_submit` para permitir un retry. Si el historial no expone ningún
`requestId`, el wrapper conserva `in_flight` y responde `502 submit_failed`: no puede demostrar la
ausencia de la operación sin arriesgar un pago duplicado. Mientras un send sigue activo en este
proceso, reconcile responde `409 send_in_progress` y tampoco cambia la key.

## Estados normalizados de operación

`GET /operations/{id}` conserva el shape de una operación e incluye `paymentState`:

| `paymentState` | Estados de Houston/Apollo |
| --- | --- |
| `pending` | `CREATED`, `SIGNING`, `SIGNED`, `BROADCASTED`, `SWAP_PENDING`, `SWAP_ROUTING`, `SWAP_OPENING_CHANNEL`, `SWAP_WAITING_CHANNEL`; también cualquier estado futuro desconocido. |
| `paid` | `CONFIRMED`, `SETTLED`, `SWAP_PAYED`. |
| `failed` | `DROPPED`, `FAILED`, `SWAP_FAILED`. |
| `expired` | `SWAP_EXPIRED`. |

La tabla replica la clasificación de `Operation.kt` de Apollo. `confirmations` continúa siendo el
valor informado por Houston para las operaciones onchain.

## Verificación de Houston

El 2026-09-28 se levantó Docker Compose contra la wallet local existente. La preview onchain de
1,000 sats respondió correctamente con `feeSatoshis: 47` y no creó una operación. Para Lightning
se creó una invoice de 1,000 sats de esa misma wallet y la preview llegó a `sswap/create`, pero
Houston devolvió HTTP 500 con `errorCode: 100000` y `Unknown error`; no era un error de sesión.

No se ejecutó `POST /send` real: la configuración local usa la ruta pública mainnet por defecto y
la wallet tenía saldo. Por seguridad no se gastaron fondos ni se intentó duplicar una operación.
Por lo tanto, Houston **no está confirmado** como deduplicador de `requestId`; el wrapper sigue
considerando su store local como la garantía de no reenvío. La prueba unitaria sí verifica que un
`requestId` determinístico que Houston devuelva en su historial cierra una key trabada. Sigue
pendiente una prueba con una wallet, fondos y destino de desarrollo explícitos para validar
`POST /send` repetido y `send-reconcile` end-to-end.
