package main

import (
	"os"

	"github.com/lmittmann/tint"
	log "log/slog"

	cli "github.com/spf13/pflag"

	"achtung/internal/achtung"
	"achtung/pkg/protocol"
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

	ptcl_cfg := protocol.PtclConfig{
		Shard:  "ACHTUNG",
		Url:    *url,
		Reconn: 5,
	}

	ptcl, err := protocol.NewProtocol(ptcl_cfg)
	if err != nil {
		log.Error("Failed to init protocol")
		os.Exit(1)
	}

	acht := achtung.NewAchtung(ptcl)

	log.Info("BOOTING UP", "url", ptcl_cfg.Url)

	go ptcl.Run()

	for {
		_, args, from := ptcl.Parse(ptcl.Receive())
		log.Info("Got msg", "args", args, "from", from)

		acht.Cmd(from, args)
	}
}
