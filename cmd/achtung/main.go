package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lmittmann/tint"
	log "log/slog"

	cli "github.com/spf13/pflag"

	"achtung/internal/achtung"
	"achtung/pkg/proto"
)

var logLevelMap = map[string]log.Level{
	"debug": log.LevelDebug,
	"info":  log.LevelInfo,
	"warn":  log.LevelWarn,
	"error": log.LevelError,
}

func main() {
	url := cli.StringP("url", "u", "ws://localhost:8092", "Url of hub")
	logLevel := cli.StringP("log", "l", "info", "Log level")
	cli.Parse()

	log.SetDefault(log.New(tint.NewHandler(os.Stdout, &tint.Options{
		Level: logLevelMap[*logLevel],
	})))

	client := proto.New("ACHTUNG", *url,
		proto.WithReconnect(5*time.Second),
	)

	acht := achtung.NewAchtung(client)

	client.Handle("*", func(req *proto.Request) {
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
