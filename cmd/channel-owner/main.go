package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/astrapath-dev/discord-bot/internal/app"
	"github.com/astrapath-dev/discord-bot/internal/config"
	"github.com/astrapath-dev/discord-bot/internal/logging"
)

var version = "dev"

func main() { os.Exit(run()) }

func run() int {
	env := flag.String("env", "", "load configuration from this env file (existing environment wins)")
	check := flag.Bool("check-config", false, "validate configuration without connecting to Discord")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("channel-owner", version)
		return 0
	}
	if err := config.LoadEnvFile(*env); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cfg, err := config.ReadOwner()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	logger := logging.New(os.Stdout, cfg.Token).With("bot", "channel-owner")
	if *check {
		logger.Info("configuration_valid", "guild_id", cfg.GuildID, "category_ids", cfg.CategoryIDs)
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.RunOwner(ctx, cfg, logger); err != nil {
		if ctx.Err() != nil {
			logger.Info("stopped")
			return 0
		}
		logger.Error("stopped_with_error", "error", err)
		return 1
	}
	logger.Info("stopped")
	return 0
}
