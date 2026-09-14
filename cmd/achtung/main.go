package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/lmittmann/tint"
	log "log/slog"

	cli "github.com/spf13/pflag"

	"achtung/internal/achtung"
	"github.com/MrZloHex/monolink"
)

// inboxSize is how many requests may wait for achtung, which takes them one
// at a time, in order.
const inboxSize = 1024

var logLevelMap = map[string]log.Level{
	"debug": log.LevelDebug,
	"info":  log.LevelInfo,
	"warn":  log.LevelWarn,
	"error": log.LevelError,
}

func loadDotEnv() {
	err := godotenv.Load()
	if err == nil {
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var pe *os.PathError
	if errors.As(err, &pe) && errors.Is(pe.Err, os.ErrNotExist) {
		return
	}
	_, _ = os.Stderr.WriteString("achtung: warning: .env: " + err.Error() + "\n")
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	loadDotEnv()

	defaultURL := envString("ACHTUNG_HUB_URL", "wss://127.0.0.1:8443")
	defaultLog := envString("ACHTUNG_LOG", "info")
	defaultCert := os.Getenv("ACHTUNG_TLS_CERT")
	defaultKey := os.Getenv("ACHTUNG_TLS_KEY")
	defaultServerCA := os.Getenv("ACHTUNG_TLS_SERVER_CA")
	// Set and empty means what it says: no persistence.
	defaultJobs := "jobs.json"
	if v, ok := os.LookupEnv("ACHTUNG_JOBS"); ok {
		defaultJobs = v
	}

	url := cli.StringP("url", "u", defaultURL, "Hub URL, wss:// only (env ACHTUNG_HUB_URL)")
	logLevel := cli.StringP("log", "l", defaultLog, "Log level (env ACHTUNG_LOG)")
	tlsCert := cli.String("tls-cert", defaultCert, "Client certificate PEM for mTLS (env ACHTUNG_TLS_CERT)")
	tlsKey := cli.String("tls-key", defaultKey, "Client private key PEM for mTLS (env ACHTUNG_TLS_KEY)")
	tlsServerCA := cli.String("tls-server-ca", defaultServerCA, "The bubble CA's PEM, which vouches for the hub (env ACHTUNG_TLS_SERVER_CA)")
	jobsPath := cli.StringP("jobs", "j", defaultJobs, "Path to job persistence file; empty disables persistence (env ACHTUNG_JOBS)")
	cli.Parse()

	level, ok := logLevelMap[*logLevel]
	if !ok {
		_, _ = os.Stderr.WriteString("achtung: log level " + *logLevel + ": debug, info, warn or error\n")
		os.Exit(2)
	}
	log.SetDefault(log.New(tint.NewHandler(os.Stdout, &tint.Options{Level: level})))

	// v2 alone: the enforcing hub carries nothing else (SPEC §44).
	tlsCfg, err := monolink.SecureTLS(*url, *tlsCert, *tlsKey, *tlsServerCA)
	if err != nil {
		log.Error("cannot reach the hub safely", "err", err)
		os.Exit(1)
	}
	opts := []monolink.Option{monolink.WithReconnect(5 * time.Second), monolink.WithDialect(monolink.V2),
		monolink.WithTLS(tlsCfg), monolink.WithInbox(inboxSize)}

	client := monolink.New(achtung.NodeName, *url, opts...)

	acht, err := achtung.NewAchtung(client, achtung.NewStore(*jobsPath))
	if err != nil {
		log.Error("cannot restore the jobs", "err", err)
		os.Exit(1)
	}
	go acht.Serve(client.Inbox())

	log.Info("BOOTING UP", "url", *url)

	if err := client.Connect(context.Background()); err != nil {
		log.Error("Failed to connect", "err", err)
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Info("SHUTTING DOWN")
	acht.Shutdown() // every change answered is saved; nothing fires after
	client.Close()
}
