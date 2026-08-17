package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"singularity/internal/bot"
	"singularity/internal/config"
	"singularity/internal/storage"
	"singularity/internal/telegram"
)

func main() {
	logger := log.New(os.Stdout, "[singularity] ", log.LstdFlags|log.Lmsgprefix)

	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("configuration error: %v", err)
	}

	db, err := storage.Open(cfg.DatabasePath)
	if err != nil {
		logger.Fatalf("database error: %v", err)
	}
	defer db.Close()

	client := telegram.NewClient(cfg.TelegramBotToken, cfg.HTTPTimeout)
	service := bot.New(cfg, db, client, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := service.Run(ctx); err != nil {
		logger.Fatalf("bot stopped: %v", err)
	}
}
