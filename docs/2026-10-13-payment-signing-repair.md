# Reparación de la firma de pagos

Esta branch contiene dos correcciones de firma. Los envíos onchain usan la
comisión de la operación en las expectativas de firma de Apollo: la deuda
esperada sigue limitando el saldo y la capacidad de pago, pero no se suma otra
vez a la comisión de la transacción. Los envíos Lightning aceptan la transacción
alternativa que Houston agrega a los swaps con funding en el piso de dust, solo
si supera la verificación estricta de la principal.

**Estado:** la corrección onchain, el fallback de alternativas Lightning y los
logs seguros están implementados y cubiertos por tests locales. La validación en
producción está pendiente: los tests locales y el build no prueban que Houston
acepte y entregue el pago. No desplegar esta branch como una reparación completa
ni recuperar órdenes mientras falten los gates operativos.

## Lightning: swaps con funding en el piso de dust

### Causa

Houston dejó de prestarle a esta wallet en invoices Lightning por debajo del
dust. En lugar de un swap `LEND` sin transacción onchain, ahora devuelve:

| Campo | Valor |
|---|---|
| `fundingOutput.outputAmountInSatoshis` | 546, elevado hasta el límite de dust |
| `debtType` | `NONE` o `COLLECT` |
| `confirmationsNeeded` | 0 |
| `maxAlternativeTransactionCount` | 1 |

El wrapper envía un grupo de nonces para la principal y uno por cada
alternativa. Houston devuelve la principal y una alternativa cuyo output de
destino también es de 546 sats. La verificación de alternativas de Apollo
(`SigningExpectations.ForAlternativeTransaction`) exige un destino estrictamente
menor que el de la principal, por lo que `Verify` de libwallet la rechazaba con
`destination amount is mismatched. found 546 expected at most 546`. En el piso de
dust no existe un destino menor. `Verify` se detiene en el destino, así que el
vuelto y la comisión de esa alternativa todavía no se observaron en producción;
la línea de log descrita en "Logs seguros" los registra.

Pedir cero alternativas no funciona: Houston responde HTTP 500 (código 100000)
cuando el cliente envía menos grupos de nonces que
`1 + maxAlternativeTransactionCount`.

### Precedente upstream

Muun corrigió este mismo caso en la libwallet de Falcon 2.16.0
([`libwallet/partiallysignedtransaction.go`](https://github.com/muun/falcon/blob/master/libwallet/partiallysignedtransaction.go)).
`verifyDestinationOutput` acepta que una alternativa pague exactamente el monto
aprobado cuando ese monto está en el piso de dust (`<= 546`). Su test de
regresión "alternative at the dust floor pays exactly the approved amount"
indica que en ese piso la alternativa y la principal son indistinguibles y ambas
deben verificar. Por encima del dust, el test "alternative just above the dust
floor pays the approved amount" mantiene el rechazo.

El wrapper se identifica ante Houston como `FALCON` 2.16.0 (`X-Client-Type`,
`X-Client-Version-Name`), pero enlaza la libwallet de Apollo, que conserva la
regla estricta (`>=`). Apollo para Android trata `Verify` como advertencia:
registra el fallo y firma igual (`LibwalletBridge.sign`). Falcon aplica, entre
otras, las verificaciones de destino y de forma de los outputs, pero deja el
monto del vuelto y la comisión como verificaciones en incubación (solo
telemetría). El wrapper no copia ninguna de esas flexibilizaciones.

### Fallback acotado

Cuando una alternativa falla `Verify` con las expectativas de alternativa y el
funding del swap está en el piso de dust (`operation.DustThreshold`, 546 sats),
el wrapper la verifica una vez más con las expectativas estrictas de la
principal: mismo monto de destino, mismo vuelto y misma comisión. Si pasa, la
alternativa se firma y se publica junto con la principal, sin costar más de lo
que permite el contrato de la principal.

- Solo un rechazo de `Verify` activa la segunda verificación. `Verify` corre
  antes de `Sign`, así que los nonces MuSig de la alternativa siguen sin usar.
- Por encima del dust no cambia nada: una alternativa igual a la principal se
  sigue rechazando, como en Falcon.
- Cualquier otro fallo sigue siendo fatal como antes: HTTP 502 `submit_failed`,
  clave `in_flight`, sin push y sin retry automático.
- Si fallan ambas verificaciones, el error incluye los dos mensajes de libwallet.
- En el piso de dust se rechaza una alternativa que pague más al destino, que
  tome comisión del vuelto o que agregue un output.

### Tests

`TestSendLightningNegotiatesAlternativesWithRealSigner` ejecuta `Verify` y `Sign`
reales de libwallet contra un Houston sintético, con inputs Taproot V5 y cofirma
MuSig, y verifica los witnesses finales con el motor de scripts de btcd. Cubre
los casos de dust de 93, 100 y 228 sats, varios inputs, deuda `COLLECT`, `LEND`,
confirmaciones, conteos de alternativas cero y negativos, alternativas no
solicitadas, principales alteradas, grupos de nonces equivocados, los límites
del fallback (destino mayor, comisión tomada del vuelto, output extra) y el
rechazo de una alternativa igual por encima del dust (547 y 10005 sats). También
verifica que las líneas de log de alternativas contengan solo números. No
reemplaza una prueba de compatibilidad y entrega contra Houston.

## Verificar el cambio

1. Ejecutar `make test` y `make build` desde la raíz del wrapper.
2. Revisar `TestSendOnchainVerifiesChangeWithoutChargingExpectedDebtTwice` y
   `TestSendOnchainRejectsTamperedChangeBeforePush`: usan transacciones
   sintéticas y los métodos reales `Verify` y `Sign`, sin un firmador que acepte
   todo.
3. Confirmar la ruta Lightning con una prueba de monto bajo autorizada y
   evidencia de entrega, no solamente un preview o un HTTP exitoso.

El contrato se compara con `buildSigningExpectations` del cliente Android de
Apollo y `PartiallySignedTransaction.Verify` en el commit fijado
`e5f5e1bb54f611ef457c56207f2a34ac85b00827`.

## Invariantes

| Área | Comportamiento que debe conservarse |
|---|---|
| Vuelto onchain | Inputs = destino + vuelto + comisión de la operación. Un vuelto alterado se rechaza antes del push. |
| Deuda | Se descuenta de la capacidad gastable; no se elimina globalmente. |
| Alternativas Lightning | El verificador de alternativas de Apollo sigue exigiendo un destino estrictamente menor, o ausencia de destino cuando el contrato lo permite. La única excepción es el piso de dust, y solo para una alternativa que supera la verificación estricta de la principal. Nunca firmar una que falle ambas. |
| Negociación | Enviar exactamente `1 + maxAlternativeTransactionCount` grupos de nonces. Rechazar más alternativas que las pedidas. |
| Ambigüedad | Un error después de crear la operación conserva `in_flight` y HTTP 502 `submit_failed`. No habilita un retry automático. |
| Replay | Una clave completada reproduce el resultado persistido, sin otro envío. |

## Logs seguros

Los fallos de creación, firma y push registran `phase`, `request_id`,
`operation_id`, `error_type` y `houston_status`. La API registra el código y el
status HTTP de un rechazo. No se escribe el mensaje completo del proveedor:
puede incluir información privada. Tampoco se registran invoice, destino, clave
de idempotencia, token, preimage ni material de wallet.

Cada alternativa que llega al fallback registra una línea, solo con números:

```text
send_alternative_check phase=<phase> request_id=<uuid> operation_id=<id> index=<n> outputs=<n> destination_delta=<sats> change_delta=<sats> fee_delta=<sats>
```

| Fase | Significado |
|---|---|
| `sign_lightning_alternative_fallback` | La verificación estricta de la principal aceptó la alternativa. |
| `sign_lightning_alternative_rejected` | Fallaron ambas verificaciones. Le sigue la línea habitual `send_failed`. |

Los deltas comparan la alternativa con el monto de destino, el vuelto y la
comisión esperados de la principal. Los valores que no se pueden decodificar se
registran como cero.

## Gates operativos antes de producción

- [ ] Imagen de candidato construida y registrada por ID/digest inmutable.
- [ ] `make test` y build de la imagen desde el checkout aprobado pasan.
- [ ] Compatibilidad Lightning demostrada con la principal y las alternativas
      negociadas, manteniendo todas las validaciones. En un swap con funding en
      el piso de dust, el push lleva la alternativa y Houston entrega el pago.
- [ ] Backup coherente de todo `data/` y secretos en almacenamiento privado,
      verificado con el wrapper detenido brevemente y vuelto a iniciar.
- [ ] Autorización explícita para reemplazar únicamente el wrapper, respetando el
      override local, volumen, secretos y binding loopback existentes.
- [ ] Smoke Lightning y onchain autorizados, con claves únicas y límite de fee;
      entrega Lightning y txid/confirmación onchain comprobados.
- [ ] Cada intento vigente de una orden en REVIEW se reconcilia antes de un retry
      autorizado. La evidencia de un intento anterior no cubre el intento actual.

Para rollback, conservar la imagen anterior y sustituir solamente la imagen del
wrapper. No restaurar automáticamente una copia vieja de `data/` después de
nuevos pagos: se perdería evidencia de idempotencia. No ejecutar dos servicios
contra el mismo directorio, crear/importar una wallet ni borrar claves.

Los saldos, claves concretas, IDs de órdenes, invoices, URLs privadas, backups y
resultados financieros pertenecen al registro operativo privado, no a este doc.
