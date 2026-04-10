███╗   ███╗ ██████╗ ███╗   ██╗ ██████╗ ██╗     ██╗████████╗██╗  ██╗
████╗ ████║██╔═══██╗████╗  ██║██╔═══██╗██║     ██║╚══██╔══╝██║  ██║
██╔████╔██║██║   ██║██╔██╗ ██║██║   ██║██║     ██║   ██║   ███████║
██║╚██╔╝██║██║   ██║██║╚██╗██║██║   ██║██║     ██║   ██║   ██╔══██║
██║ ╚═╝ ██║╚██████╔╝██║ ╚████║╚██████╔╝███████╗██║   ██║   ██║  ██║
╚═╝     ╚═╝ ╚═════╝ ╚═╝  ╚═══╝ ╚═════╝ ╚══════╝╚═╝   ╚═╝   ╚═╝  ╚═╝


  ░▒▓█ _achtung_ █▓▒░
  The timekeeper - so you never miss a moment.

  ───────────────────────────────────────────────────────────────
  ▓ OVERVIEW
  **achtung** is a MONOLITH **node** written in **Go**.
  ▪ Connects to **concentrator** over WebSocket (`ws://` or `wss://` with optional mTLS)
  ▪ Runs one-shot timers and absolute-time alarms from wire commands
  ▪ On fire, drives **vertex** (buzzer) and broadcasts to the network

  ───────────────────────────────────────────────────────────────
  ▓ ARCHITECTURE
  ▪ **RUNTIME**: Go 1.24+ (see `go.mod`)
  ▪ **TRANSPORT**: WebSocket (`github.com/MrZloHex/monolink`); optional **mTLS** to a `wss://` hub
  ▪ **SCHEDULER**: Min-heap priority queue, single-goroutine event loop
  ▪ **NODE ID**: `ACHTUNG`

  ───────────────────────────────────────────────────────────────
  ▓ FEATURES
  ▪ One-shot timers (relative duration)
  ▪ One-shot alarms (absolute date and time in local timezone)
  ▪ Buzzer integration via **vertex**
  ▪ Auto-reconnect on WebSocket disconnect
  ▪ Uptime reporting
  ▪ Ping/pong health check
  ▪ Graceful shutdown on SIGINT/SIGTERM
  ▪ Optional mTLS (client certificate) when using `wss://`
  (Repeating intervals exist internally but are not exposed on the wire yet.)

  ───────────────────────────────────────────────────────────────
  ▓ REQUIREMENTS
  ▪ Go 1.24+ (see `go.mod`)

  ───────────────────────────────────────────────────────────────
  ▓ BUILD & RUN
  **Build**
  ```sh
  go build -o bin/achtung ./cmd/achtung
  ```

  **Run**
  ```sh
  ./bin/achtung
  ```
  Defaults: hub `ws://localhost:8092`, log **info** — see **CONFIGURATION**.

  **Example** (plain WebSocket)
  ```sh
  ./bin/achtung -u ws://localhost:8092 -l info
  ```

  **Example** (`wss://` + mTLS; paths may also come from `.env`)
  ```sh
  ./bin/achtung -u wss://hub.example:8092 \
    --tls-cert /path/to/client.crt --tls-key /path/to/client.key
  # optional: --tls-server-ca /path/to/ca.pem
  ```

  ───────────────────────────────────────────────────────────────
  ▓ CONFIGURATION
  On startup, **achtung** loads a `.env` file from the current working directory if it exists (`godotenv`). Missing `.env` is fine; other read errors print a warning to stderr and the process continues. Environment variables supply **defaults for flags**; CLI arguments override them.

  **Environment**
  ▪ `ACHTUNG_HUB_URL` — hub WebSocket URL (default `ws://localhost:8092`)
  ▪ `ACHTUNG_LOG` — `debug`, `info`, `warn`, `error` (default `info`)
  ▪ `ACHTUNG_TLS_CERT` — client certificate PEM (mTLS)
  ▪ `ACHTUNG_TLS_KEY` — client private key PEM (mTLS)
  ▪ `ACHTUNG_TLS_SERVER_CA` — optional PEM CA for the hub server cert; omit for system trust store

  With mTLS, the URL must be **`wss://`** and both **`ACHTUNG_TLS_CERT`** and **`ACHTUNG_TLS_KEY`** must be set (or equivalent `--tls-cert` / `--tls-key`).

  **Flags**
  ▪ `-u`, `--url` — hub URL (`ACHTUNG_HUB_URL`)
  ▪ `-l`, `--log` — log level (`ACHTUNG_LOG`)
  ▪ `--tls-cert` — client certificate PEM (`ACHTUNG_TLS_CERT`)
  ▪ `--tls-key` — client private key PEM (`ACHTUNG_TLS_KEY`)
  ▪ `--tls-server-ca` — optional hub server CA PEM (`ACHTUNG_TLS_SERVER_CA`)

  ───────────────────────────────────────────────────────────────
  ▓ PROTOCOL
  Packet format: `<TO>:<VERB>:<NOUN>[:<ARGS>...]:<FROM>`

  First field **TO** must be **`ACHTUNG`** for the daemon to handle the message (see `cmd/achtung`). Replies use **`FROM=ACHTUNG`** (e.g. `<peer>:OK:TIMER:<name>:ACHTUNG`). Errors use verb **`ERR`**.

  ─── PING ───
  `ACHTUNG:PING:PING:<from>` → `<from>:PONG:PONG:ACHTUNG`

  ─── NEW ───
  `ACHTUNG:NEW:TIMER:<name>:<duration>:<from>` → `<from>:OK:TIMER:<name>:ACHTUNG`
  `<duration>` — Go duration (e.g. `10s`, `2h30m`).

  `ACHTUNG:NEW:ALARM:<name>:<date>:<time>:<from>` → `<from>:OK:ALARM:<name>:ACHTUNG`
  `<date>` — `Y.M.D` (e.g. `2026.4.9`). `<time>` — `H.M` in local time (e.g. `14.30`).

  ─── STOP ───
  `ACHTUNG:STOP:TIMER:<name>:<from>` / `ACHTUNG:STOP:ALARM:<name>:<from>`
  → `<from>:OK:TIMER:<name>:ACHTUNG` or `<from>:OK:ALARM:<name>:ACHTUNG`
  Also sends `VERTEX:OFF:BUZZ:ACHTUNG` to silence the buzzer.

  ─── GET ───
  `ACHTUNG:GET:LIST:<from>` → `<from>:OK:LIST:ACHTUNG` or `<from>:OK:LIST:<kind>:<name>:...:ACHTUNG`
  `ACHTUNG:GET:JOB:<name>:<from>` → `<from>:OK:JOB:<kind>:<name>:<remaining>:<due>:ACHTUNG` (`<due>` = `Y.M.D:H.M` local)
  `ACHTUNG:GET:UPTIME:<from>` → `<from>:OK:UPTIME:<duration>:ACHTUNG`

  ─── EVENTS (outbound) ───
  When a job fires, **achtung** sends:
  ▪ `ALL:FIRE:<KIND>:<name>:ACHTUNG` — broadcast
  ▪ `VERTEX:ON:BUZZ:ACHTUNG` — buzzer on

  ───────────────────────────────────────────────────────────────
  ▓ HUB (CONCENTRATOR)
  The WebSocket hub is a separate binary (the **concentrator** module). It uses **`CONCENTRATOR_*`** for listen address and server TLS; see the concentrator README.

  ───────────────────────────────────────────────────────────────
  ▓ FINAL WORDS
  This is not just a timer.
