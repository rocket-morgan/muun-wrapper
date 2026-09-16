# Plan: muun-wrapper - wallet backend headless sobre Muun Apollo

Fecha: 2026-09-15
Estado: propuesto (pendiente de aprobacion)

## Objetivo

Crear un wrapper que funcione como cliente CLI + API de los servicios de la wallet Muun,
equivalente funcional a la app mobile Apollo:

- Generar una seed
- Enviar y recibir Lightning
- Enviar y recibir onchain
- Saldo unificado (lo recibido onchain se puede gastar por LN y viceversa, con swaps
  invisibles para el usuario)

Todo expuesto via CLI y API HTTP, empaquetado en un container Docker que expone la API.

## Hallazgos del analisis del repo apollo (~/projects/wapu/apollo)

- La app Android (Kotlin) contiene UI y parte de la logica de sesion.
- **libwallet** es un modulo Go independiente (github.com/muun/libwallet, con su propio
  go.mod) donde vive toda la logica de wallet:
  - Generacion de seed/keys, addresses, firma de transacciones 2-de-2 (MuSig2)
  - Submarine swaps v1/v2 (enviar Lightning)
  - Incoming swaps (recibir Lightning)
  - Invoices, LNURL, BIP21
  - Cliente HTTP a Houston (backend de Muun) en service/
  - Maquina de estados de sincronizacion de operaciones en newop/
  - Abstracciones de storage (storage/, walletdb/) y encriptacion de claves (keycrypter)
- Es el mismo core que usan las apps mobile via gomobile.
- Existe un presentation/wallet_server.go con un servicio gRPC, pero esta orientado a
  security cards / diagnostico / recovery, no a la API completa de wallet.

## Decision de lenguaje: Go

- Python implicaria reimplementar criptografia, swaps y firma MuSig2 desde cero. Descartado.
- Kotlin: el core igual es Go (gomobile). Descartado.
- Go permite consumir libwallet como libreria; el wrapper pasa a ser codigo de orquestacion.

## Arquitectura: wrapper externo (sin modificar apollo)

Modulo nuevo muun-wrapper en este directorio:

- require github.com/muun/libwallet con replace apuntando a
  ~/projects/wapu/apollo/libwallet para desarrollo local.
- Para builds reproducibles (Docker), pinear a un commit/tag y clonar apollo dentro del build.
- Ventaja: se pueden seguir bajando updates de Muun con git pull + ajustes menores de glue.

El wrapper implementa la capa de aplicacion que hoy vive en la app mobile:

1. **Ciclo de vida de wallet**: generar seed, derivar user key / muun key, crear sesion con
   Houston, setup de challenge keys, login y refresh de JWT (replicando el flujo de auth de
   apollo, usando el codigo Kotlin como referencia).
2. **Storage headless**: implementar las interfaces de storage/ y walletdb/ sobre SQLite
   en un directorio de datos; clave privada encriptada con passphrase via keycrypter de
   libwallet.
3. **Sync de saldo unificado**: loop de polling al endpoint de operaciones de Houston corriendo
   la maquina newop, mas UTXOs. El polling reemplaza las push notifications (FCM) de la app.
4. **Recibir onchain**: proxima address via el esquema de cosigners de libwallet.
5. **Recibir Lightning**: incoming swap via Houston -> devuelve invoice bolt11; el poller
   detecta cuando se completa.
6. **Enviar Lightning**: submarine swap v2 (parse de invoice, Houston arma el swap, se paga
   onchain al HTLC). Swap invisible para el usuario, igual que en la app.
7. **Enviar onchain**: fee estimation de Houston, armado y firma del PSBT con libwallet,
   broadcast via Houston.
8. **Superficie doble**:
   - API REST simple (net/http, sin framework pesado):
     - POST /wallet/create
     - GET /balance
     - POST /send (address onchain o invoice bolt11)
     - POST /receive/invoice
     - GET /receive/address
     - GET /operations
   - CLI (cobra o stdlib) con los mismos comandos, hablando contra la API.
   - Auth de la API por token estatico.
9. **Docker**: imagen multi-stage (builder con Go + librs/makelibs.sh, la dependencia Rust
   de libwallet), runtime minimo, volumen para el datadir, puerto expuesto para la API.
   docker compose opcional.

## Orden de trabajo

1. Esqueleto del modulo + compilacion de libwallet como dependencia
2. Create/login de sesion contra Houston
3. Sync + balance
4. Receive onchain / LN
5. Send onchain / LN
6. CLI
7. Docker + README

## Supuestos

- Single-wallet: una instancia = un datadir = una seed. API protegida con token estatico.
- Sin manejo de email/identidad en v1 (opcional en Muun).
- Sin emergency kit / recovery code flow en v1.

## Riesgos

- **Houston no es una API publica versionada**: si Muun cambia algo, hay que adaptar el
  wrapper (mitigado por el approach wrapper + updates).
- **Testing real**: Houston publico es mainnet. libwallet soporta regtest/signet
  internamente, pero las pruebas end-to-end van a ser con montos chicos reales.
- **Auth flow**: los detalles (headers, challenge signing) se replican leyendo el codigo
  Kotlin; es trabajo de lectura, no de investigacion a ciegas, porque el codigo fuente esta.
- **Dependencia Rust**: libwallet requiere compilar librs (Cargo); hay que replicar eso en
  el Dockerfile del builder.
