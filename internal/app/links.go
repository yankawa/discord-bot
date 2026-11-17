package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/astrapath-dev/discord-bot/internal/config"
	"github.com/astrapath-dev/discord-bot/internal/discordapi"
	"github.com/astrapath-dev/discord-bot/internal/links"
	"github.com/astrapath-dev/discord-bot/internal/processlock"
)

func RunLinks(parent context.Context, cfg config.Links, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	release, err := processlock.Acquire(cfg.StateFile)
	if err != nil {
		return err
	}
	defer release()
	store, err := links.OpenStore(cfg.StateFile, cfg.GuildID, time.Now())
	if err != nil {
		return err
	}
	trigger := make(chan struct{}, 1)
	fatal := make(chan error, 1)
	var queue *linkQueue
	api, err := discordapi.NewLinks(cfg.Token, cfg.GuildID, discordapi.LinkHandlers{
		Ready: func(botID string) {
			logger.Info("gateway_ready", "bot_id", botID, "guild_id", cfg.GuildID)
			triggerScan(trigger)
		},
		Fatal: func(err error) {
			select {
			case fatal <- err:
			default:
			}
		},
		MessageCreate: func(msg links.Message) { queue.create(msg) },
		MessageUpdate: func(channelID, messageID string) { queue.update(channelID, messageID) },
		MessageDelete: func(channelID, messageID string) { queue.delete(channelID, messageID) },
	}, logger)
	if err != nil {
		return fmt.Errorf("initialize Discord client: %w", err)
	}
	api.RequestTimeout = cfg.RequestTimeout
	var background sync.WaitGroup
	defer func() {
		cancel()
		if queue != nil {
			queue.wait()
		}
		background.Wait()
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		api.Close(closeCtx)
	}()
	botID, err := api.BotID(ctx)
	if err != nil {
		return err
	}
	if err := api.Validate(ctx, cfg.ChannelIDs); err != nil {
		return fmt.Errorf("link permission preflight: %w", err)
	}
	controller, err := links.NewController(links.Config{
		GuildID: cfg.GuildID, ChannelIDs: cfg.ChannelIDs,
		MaxLinks: cfg.MaxLinks, Retention: cfg.Retention,
	}, store, api)
	if err != nil {
		return err
	}
	slots := make(chan struct{}, cfg.Workers)
	queue = newLinkQueue(ctx, cfg.Workers, cfg.QueueCapacity, controller, api, logger, slots)
	scan := func(ctx context.Context) {
		records := controller.Records()
		channels := make(map[string]string, len(records))
		ids := make([]string, 0, len(records))
		for _, rec := range records {
			channels[rec.MessageID] = rec.ChannelID
			ids = append(ids, rec.MessageID)
		}
		parallel(ctx, cfg.Workers, ids, func(ctx context.Context, id string) {
			withSlot(ctx, slots, func() {
				if err := controller.Refresh(ctx, channels[id], id); err != nil && ctx.Err() == nil {
					logger.Error("link_reconcile_failed", "message_id", id, "error", err)
				}
			})
		})
	}
	background.Add(1)
	go func() { defer background.Done(); periodic(ctx, cfg.ReconcileInterval, trigger, scan) }()
	logger.Info("links_starting", "bot_id", botID, "guild_id", cfg.GuildID,
		"channel_ids", cfg.ChannelIDs, "tracked_messages", len(controller.Records()), "retention_days", int(cfg.Retention.Hours()/24))
	if err := api.Open(ctx); err != nil {
		return fmt.Errorf("open Discord gateway: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-fatal:
		return fmt.Errorf("Discord gateway stopped: %w", err)
	}
}
