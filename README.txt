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
  It connects to a _concentrator_ hub via WebSocket,  
  accepting commands to set timers, alarms, and repeating intervals.  
  When a job fires, it triggers a buzzer on _vertex_ and broadcasts  
  a notification to the network.  
  Punctual. Persistent. Loud.  

  ───────────────────────────────────────────────────────────────  
  ▓ ARCHITECTURE  
  ▪ **RUNTIME**: Go 1.24  
  ▪ **TRANSPORT**: WebSocket (gorilla/websocket) via pkg/proto client  
  ▪ **SCHEDULER**: Min-heap priority queue, single-goroutine event loop  
  ▪ **NODE ID**: ACHTUNG  

  ───────────────────────────────────────────────────────────────  
  ▓ FEATURES  
  ▪ One-shot timers (relative duration)  
  ▪ One-shot alarms (absolute date & time)  
  ▪ Repeating intervals  
  ▪ Buzzer integration via _vertex_  
  ▪ Auto-reconnect on WebSocket disconnect  
  ▪ Uptime reporting  
  ▪ Ping/pong health check  
  ▪ Graceful shutdown on SIGINT/SIGTERM  

  ───────────────────────────────────────────────────────────────  
  ▓ BUILD & RUN  
  ```sh  
  go build -o bin/achtung ./cmd/achtung  
  ./bin/achtung -u ws://localhost:8092 -l info  
  ```

  Flags:  
  ▪ `-u`  WebSocket hub URL  (default: ws://localhost:8092)  
  ▪ `-l`  Log level: debug, info, warn, error  (default: info)  

  ───────────────────────────────────────────────────────────────  
  ▓ PROTOCOL  
  Packet format:  <TO>:<VERB>:<NOUN>[:<ARGS>...]:<FROM>  

  Responses:  OK:<NOUN>[:ARGS]  or  ERR:<REASON>[:ARGS]  

  ─── PING ───  
  PING:PING                        -> PONG:PONG  

  ─── NEW ───  
  NEW:TIMER:<name>:<duration>      -> OK:TIMER:<name>  
  NEW:ALARM:<name>:<date>:<time>   -> OK:ALARM:<name>  

  ─── STOP ───  
  STOP:TIMER:<name>                -> OK:TIMER:<name>  
  STOP:ALARM:<name>                -> OK:ALARM:<name>  

  ─── GET ───  
  GET:LIST                         -> OK:LIST[:<kind>:<name>:...]  
  GET:JOB:<name>                   -> OK:JOB:<kind>:<name>:<remaining>:<due>  
  GET:UPTIME                       -> OK:UPTIME:<duration>  

  ─── EVENTS ───  
  When a job fires, _achtung_ emits:  
  ▪ ALL:FIRE:<KIND>:<name>         (broadcast notification)  
  ▪ VERTEX:ON:BUZZ                 (activate buzzer)  

  ───────────────────────────────────────────────────────────────  
  ▓ FINAL WORDS  
  This is not just a timer.  
