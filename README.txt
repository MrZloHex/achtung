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
  **achtung** is a MONOLITH **node** at `ACHTUNG`, written in **Go**,
  speaking monolink v2 only (SPEC.txt §27).
  ▪ Timers, alarms, repeating and daily jobs, kept across restarts
  ▪ When one fires: ALL:FIRE for whoever hears it, and vertex's buzzer on
  ▪ Publishes JOBS.COUNT and NEXT, when the next job fires

  It serves whoever the hub lets through. The enforcing concentrator holds
  each panel to its person's grants — ACHTUNG.NEW.*, ACHTUNG.STOP.*,
  ACHTUNG.GET.* — and each node to its policy line (SECURITY.txt §5);
  achtung checks no identity of its own. Jobs are the household's, not
  whoever made them: anyone allowed to STOP may stop any.

  ───────────────────────────────────────────────────────────────
  ▓ BUILD & RUN
  ```sh
  go build -o bin/achtung ./cmd/achtung
  go test ./...
  ./bin/achtung --tls-cert achtung.pem --tls-key achtung.key --tls-server-ca bubble-ca.pem
  ```

  ───────────────────────────────────────────────────────────────
  ▓ CONFIGURATION
  A `.env` in the working directory supplies defaults for flags; flags win.

    -u, --url            ACHTUNG_HUB_URL         wss://127.0.0.1:8443
    -j, --jobs           ACHTUNG_JOBS            jobs.json   (empty: kept in memory only)
        --tls-cert       ACHTUNG_TLS_CERT
        --tls-key        ACHTUNG_TLS_KEY
        --tls-server-ca  ACHTUNG_TLS_SERVER_CA   the bubble CA
    -l, --log            ACHTUNG_LOG             info   (debug, info, warn, error)

  It does not start without a wss:// URL and all three TLS files: nodes
  trust whoever the hub says sent a frame, so anything else at the hub's
  port could arm and stop anything. An unknown log level is refused, and
  so is a jobs file that cannot be right (STATE).

  ───────────────────────────────────────────────────────────────
  ▓ PROTOCOL
  v2 frames (`2:<id>:<from>:ACHTUNG:<verb>:<noun>[:<arg>...]`).

    NEW:TIMER:<name>:<duration>      -> OK:TIMER:<name>
    NEW:ALARM:<name>:<date>:<time>   -> OK:ALARM:<name>
    NEW:EVERY:<name>:<duration>      -> OK:EVERY:<name>
    NEW:DAILY:<name>:<time>          -> OK:DAILY:<name>
    STOP:<kind>:<name>               -> OK:<kind>:<name>
    GET:LIST[:<after>]               -> OK:LIST[:<kind>:<name>...]
    GET:JOB:<name>                   -> OK:JOB:<kind>:<name>:<remaining>:<due>
    GET:JOBS.COUNT | NEXT | UPTIME | VERSION, PING   the object model's

  ▪ <duration> is a Go duration (10s, 2h30m): a TIMER from a second to a
    year, an EVERY from a minute to a year. <date> is YYYY.MM.DD, <time>
    H.M (H:M typed by hand is read too), local; nothing may follow either.
    An ALARM is after now and within ten years. A time the clocks skip
    that day comes the gap later; one they repeat, once.
  ▪ A <name> is what a person calls the job: at most 64 bytes, one line,
    nothing invisible. NEW replaces a job of the same name — so a panel
    edits a job by making it again. achtung keeps 256 jobs at most.
  ▪ Repeating jobs skip occurrences missed while achtung was down rather
    than replaying them; one-shots whose time passed meanwhile are dropped.
  ▪ STOP removes the job of that kind and name — a STOP:TIMER does not
    remove a DAILY — and silences the buzzer, also when there is no such
    job any more: a one-shot that fired is gone, and STOP is how its
    ringing ends. No job fired before a STOP sounds the buzzer after it.
  ▪ GET:LIST gives jobs by name, eight to a reply (a kind and a name each,
    sixteen arguments to a frame). Ask again with <after>, the last name
    you got, until the answer is empty.
  ▪ <remaining> is a Go duration; <due> is YYYY.MM.DD.HH.MM.
  ▪ Requests are taken one at a time, in the order they arrived: a NEW
    then a STOP are never done the other way round.

  When a job fires, achtung sends `ALL:FIRE:<kind>:<name>` and
  `VERTEX:SET:BUZZ.STATE:ON` — the property uart2ws registers for vertex.
  While the hub is out of reach, as when achtung starts before it has
  connected, it tries again every two seconds, for up to ten minutes: a
  hub restarting is waited out, an alarm an hour late is not rung. Its
  policy line lets it send `ALL.FIRE.* VERTEX.SET.BUZZ.STATE`.

  Errors: ARGC, ARG (a name, or not a job), DUR, TIME, NOUN, VERB, NAC (no
  such job, or one of another kind), BUSY (as many jobs as achtung keeps,
  or shutting down), STATE (could not save; or the buzzer not told OFF —
  STOP again).

  ───────────────────────────────────────────────────────────────
  ▓ STATE
  jobs.json, mode 0600. Each change is saved before it is answered, by
  writing a new file beside the old and renaming it over: a crash or a full
  disk leaves the old file or the new, never half of one, and a change
  whose save failed is not made. What achtung changes by itself — a job
  fired, a repeating one moved on — it saves at once, and if that fails,
  again every ten seconds. A file that cannot be right — larger than
  achtung writes, two jobs of one name, a job that is no job — stops
  achtung with the file untouched, rather than let the next change write
  over it with what was left.

  ───────────────────────────────────────────────────────────────
  ▓ DEPLOY
  Through deploy/'s monolithctl, with MONOLITH's release: its own user, a
  sandboxed unit, its key sealed, jobs.json in /var/lib/monolith/achtung
  (deploy/README.txt).

  ───────────────────────────────────────────────────────────────
  ▓ FINAL WORDS
  This is not just a timer.
