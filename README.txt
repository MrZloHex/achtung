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
  **_achtung_** is a timer & alarm microservice written in **Go**.  
  It connects to a _concentrator_ hub over **WebSocket** (plain **ws://** or  
  **wss://** with optional **mTLS**), and accepts commands to create and  
  manage one-shot timers and absolute-time alarms.  
  When a job fires, it triggers a buzzer on _vertex_ and broadcasts  
  a notification to the network.  
  Punctual. Persistent. Loud.  

  ───────────────────────────────────────────────────────────────  
  ▓ ARCHITECTURE  
  ▪ **RUNTIME**: Go 1.24  
  ▪ **TRANSPORT**: WebSocket (gorilla/websocket) via pkg/proto; TLS client  
    certs supported for mTLS to a **wss://** hub  
  ▪ **SCHEDULER**: Min-heap priority queue, single-goroutine event loop  
  ▪ **NODE ID**: ACHTUNG  

  ───────────────────────────────────────────────────────────────  
  ▓ FEATURES  
  ▪ One-shot timers (relative duration)  
  ▪ One-shot alarms (absolute date & time in local timezone)  
  ▪ Buzzer integration via _vertex_  
  ▪ Auto-reconnect on WebSocket disconnect  
  ▪ Uptime reporting  
  ▪ Ping/pong health check  
  ▪ Graceful shutdown on SIGINT/SIGTERM  
  ▪ Optional mTLS (client certificate) when using **wss://**  

  (The scheduler also supports repeating intervals internally; that is not  
  exposed as a wire command yet.)  

  ───────────────────────────────────────────────────────────────  
  ▓ BUILD  
  ```sh  
  go build -o bin/achtung ./cmd/achtung  
  ```  

  ───────────────────────────────────────────────────────────────  
  ▓ CONFIGURATION  
  Copy **`.env.example`** to **`.env`** and adjust paths, or set the same  
  variables in the environment.  

  ▪ **`ACHTUNG_HUB_URL`** — hub WebSocket URL (default `ws://localhost:8092`)  
  ▪ **`ACHTUNG_LOG`** — debug, info, warn, error (default `info`)  
  ▪ **`ACHTUNG_TLS_CERT`** — client certificate PEM (mTLS)  
  ▪ **`ACHTUNG_TLS_KEY`** — client private key PEM  
  ▪ **`ACHTUNG_TLS_SERVER_CA`** — optional PEM CA for the hub’s server cert;  
    omit to use the system trust store  

  With mTLS, the URL must be **`wss://`** and both **`ACHTUNG_TLS_CERT`** and  
  **`ACHTUNG_TLS_KEY`** must be set (or **`--tls-cert`** / **`--tls-key`**).  

  ───────────────────────────────────────────────────────────────  
  ▓ RUN  
  Plain WebSocket (no TLS):  
  ```sh  
  ./bin/achtung -u ws://localhost:8092 -l info  
  ```  

  TLS + mTLS (paths can also come from `.env`):  
  ```sh  
  ./bin/achtung -u wss://hub.example:8092 \  
    --tls-cert /path/to/client.crt --tls-key /path/to/client.key  
  # optional: --tls-server-ca /path/to/ca.pem  
  ```  

  **Flags** (environment defaults in parentheses):  
  ▪ `-u`, `--url` — hub URL (`ACHTUNG_HUB_URL`)  
  ▪ `-l`, `--log` — log level (`ACHTUNG_LOG`)  
  ▪ `--tls-cert` — client certificate PEM (`ACHTUNG_TLS_CERT`)  
  ▪ `--tls-key` — client private key PEM (`ACHTUNG_TLS_KEY`)  
  ▪ `--tls-server-ca` — optional hub server CA PEM (`ACHTUNG_TLS_SERVER_CA`)  

  ───────────────────────────────────────────────────────────────  
  ▓ PROTOCOL  
  Packet format:  <TO>:<VERB>:<NOUN>[:<ARGS>...]:<FROM>  

  First field **TO** must be **`ACHTUNG`** for the daemon to handle the message  
  (see `cmd/achtung`). Replies are addressed back to the sender with  
  **`FROM=ACHTUNG`** (e.g. `<peer>:OK:TIMER:<name>:ACHTUNG`). Errors use verb **`ERR`**.  

  ─── PING ───  
  `ACHTUNG:PING:PING:<from>`  →  `<from>:PONG:PONG:ACHTUNG`  

  ─── NEW ───  
  `ACHTUNG:NEW:TIMER:<name>:<duration>:<from>`  →  `<from>:OK:TIMER:<name>:ACHTUNG`  
    `<duration>` — Go duration (e.g. `10s`, `2h30m`).  

  `ACHTUNG:NEW:ALARM:<name>:<date>:<time>:<from>`  →  `<from>:OK:ALARM:<name>:ACHTUNG`  
    `<date>` — `Y.M.D` (e.g. `2026.4.9`).  
    `<time>` — `H.M` in local time (e.g. `14.30`).  

  ─── STOP ───  
  `ACHTUNG:STOP:TIMER:<name>:<from>`  /  `ACHTUNG:STOP:ALARM:<name>:<from>`  
    →  `<from>:OK:TIMER:<name>:ACHTUNG` or `<from>:OK:ALARM:<name>:ACHTUNG`  
  Also sends **`VERTEX:OFF:BUZZ:ACHTUNG`** to silence the buzzer.  

  ─── GET ───  
  `ACHTUNG:GET:LIST:<from>`  →  `<from>:OK:LIST:ACHTUNG` or  
    `<from>:OK:LIST:<kind>:<name>:...:ACHTUNG`  
  `ACHTUNG:GET:JOB:<name>:<from>`  →  `<from>:OK:JOB:<kind>:<name>:<remaining>:<due>:ACHTUNG`  
    `<due>` — `Y.M.D:H.M` (local time).  
  `ACHTUNG:GET:UPTIME:<from>`  →  `<from>:OK:UPTIME:<duration>:ACHTUNG`  

  ─── EVENTS (outbound) ───  
  When a job fires, _achtung_ sends:  
  ▪ `ALL:FIRE:<KIND>:<name>:ACHTUNG` — broadcast  
  ▪ `VERTEX:ON:BUZZ:ACHTUNG` — buzzer on  

  ───────────────────────────────────────────────────────────────  
  ▓ HUB (CONCENTRATOR)  
  The WebSocket **hub** is a separate service (e.g. the **concentrator**  
  module). It typically loads **`CONCENTRATOR_*`** TLS settings from `.env`;  
  see **`.env.example`** in this repo for variable names shared with the hub.  

  ───────────────────────────────────────────────────────────────  
  ▓ FINAL WORDS  
  This is not just a timer.  
