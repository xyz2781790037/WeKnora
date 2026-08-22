package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/Tencent/WeKnora/internal/pluginruntime"
)

func main() {
	config, err := pluginruntime.LoadConfig()
	if err != nil {
		log.Fatalf("load plugin runtime config: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("plugin-runtime listening on %s", config.ListenAddr)
	if err := pluginruntime.Run(ctx, config); err != nil {
		log.Fatalf("plugin-runtime stopped with error: %v", err)
	}
}
