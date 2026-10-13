# Reparación de la firma de pagos

La corrección onchain usa la comisión de la operación en las expectativas de
firma de Apollo. La deuda esperada sigue limitando el saldo y la capacidad de
pago; no se suma otra vez a la comisión de la transacción.

**Estado:** corrección onchain y logs implementados. La ruta Lightning de salida
mínima está pendiente de demostrar compatibilidad con Houston. No desplegar esta
branch como una reparación completa ni recuperar órdenes mientras falten esos
gates.

## Verificar el cambio

1. Ejecutar `make test` y `make build` desde la raíz del wrapper.
2. Revisar `TestSendOnchainVerifiesChangeWithoutChargingExpectedDebtTwice` y
   `TestSendOnchainRejectsTamperedChangeBeforePush`: usan transacciones sintéticas
   y los métodos reales `Verify` y `Sign`, sin un firmador que acepte todo.
3. Confirmar la ruta Lightning con una prueba de monto bajo autorizada y evidencia
   de entrega, no solamente un preview o un HTTP exitoso.

El contrato se compara con `buildSigningExpectations` del cliente Android de
Apollo y `PartiallySignedTransaction.Verify` en el commit fijado
`e5f5e1bb54f611ef457c56207f2a34ac85b00827`.

## Invariantes

| Área | Comportamiento que debe conservarse |
|---|---|
| Vuelto onchain | Inputs = destino + vuelto + comisión de la operación. Un vuelto alterado se rechaza antes del push. |
| Deuda | Se descuenta de la capacidad gastable; no se elimina globalmente. |
| Alternativas Lightning | El verificador de Apollo sigue exigiendo un destino estrictamente menor, o ausencia de destino cuando el contrato lo permite. No relajar esa validación. |
| Negociación | Cero alternativas es una hipótesis pendiente, no una compatibilidad probada por un mock. |
| Ambigüedad | Un error después de crear la operación conserva `in_flight` y HTTP 502 `submit_failed`. No habilita un retry automático. |
| Replay | Una clave completada reproduce el resultado persistido, sin otro envío. |

## Logs seguros

Los fallos de creación, firma y push registran `phase`, `request_id`,
`operation_id`, `error_type` y `houston_status`. La API registra el código y el
status HTTP de un rechazo. No se escribe el mensaje completo del proveedor:
puede incluir información privada. Tampoco se registran invoice, destino, clave
de idempotencia, token, preimage ni material de wallet.

## Gates operativos antes de producción

- [ ] Imagen de candidato construida y registrada por ID/digest inmutable.
- [ ] `make test` y build de la imagen desde el checkout aprobado pasan.
- [ ] Compatibilidad Lightning demostrada con la principal y las alternativas
      negociadas, manteniendo todas las validaciones.
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
