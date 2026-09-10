package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/lmittmann/tint"
	log "log/slog"

	cli "github.com/spf13/pflag"

	"achtung/internal/achtung"
	"github.com/MrZloHex/monolink"
)

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

	defaultURL := envString("ACHTUNG_HUB_URL", "ws://localhost:8092")
	defaultLog := envString("ACHTUNG_LOG", "info")
	defaultCert := os.Getenv("ACHTUNG_TLS_CERT")
	defaultKey := os.Getenv("ACHTUNG_TLS_KEY")
	defaultServerCA := os.Getenv("ACHTUNG_TLS_SERVER_CA")
	defaultJobs := envString("ACHTUNG_JOBS", "jobs.json")

	url := cli.StringP("url", "u", defaultURL, "WebSocket hub URL (env ACHTUNG_HUB_URL; use wss:// with mTLS)")
	logLevel := cli.StringP("log", "l", defaultLog, "Log level (env ACHTUNG_LOG)")
	tlsCert := cli.String("tls-cert", defaultCert, "Client certificate PEM for mTLS (env ACHTUNG_TLS_CERT)")
	tlsKey := cli.String("tls-key", defaultKey, "Client private key PEM for mTLS (env ACHTUNG_TLS_KEY)")
	tlsServerCA := cli.String("tls-server-ca", defaultServerCA, "Optional PEM CA for hub server cert; empty uses system roots (env ACHTUNG_TLS_SERVER_CA)")
	jobsPath := cli.StringP("jobs", "j", defaultJobs, "Path to job persistence file; empty disables persistence (env ACHTUNG_JOBS)")
	cli.Parse()

	log.SetDefault(log.New(tint.NewHandler(os.Stdout, &tint.Options{
		Level: logLevelMap[*logLevel],
	})))

	opts := []monolink.Option{monolink.WithReconnect(5 * time.Second)}
	if *tlsCert != "" || *tlsKey != "" || *tlsServerCA != "" {
		if *tlsCert == "" || *tlsKey == "" {
			log.Error("mTLS requires both --tls-cert and --tls-key (or ACHTUNG_TLS_CERT and ACHTUNG_TLS_KEY)")
			os.Exit(1)
		}
		if !strings.HasPrefix(*url, "wss://") {
			log.Error("mTLS requires a wss:// hub URL", "url", *url)
			os.Exit(1)
		}
		tlsCfg, err := monolink.LoadClientTLS(*tlsCert, *tlsKey, *tlsServerCA)
		if err != nil {
			log.Error("TLS client configuration failed", "err", err)
			os.Exit(1)
		}
		opts = append(opts, monolink.WithTLS(tlsCfg))
	}

	client := monolink.New("ACHTUNG", *url, opts...)

	acht := achtung.NewAchtung(client, achtung.NewStore(*jobsPath))

	client.Handle("*", func(req *monolink.Request) {
		if req.Msg.To != client.NodeID() {
			return
		}
		acht.Cmd(req)
	})

	log.Info("BOOTING UP", "url", *url)

	if err := client.Connect(context.Background()); err != nil {
		log.Error("Failed to connect", "err", err)
		os.Exit(1)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Info("SHUTTING DOWN")
	acht.Shutdown()
	client.Close()
}
