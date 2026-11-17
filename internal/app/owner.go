package app

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/astrapath-dev/discord-bot/internal/config"
	"github.com/astrapath-dev/discord-bot/internal/discordapi"
	"github.com/astrapath-dev/discord-bot/internal/owner"
	"github.com/astrapath-dev/discord-bot/internal/processlock"
)

func RunOwner(parent context.Context, cfg config.Owner, logger *slog.Logger) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	release, err := processlock.Acquire(cfg.StateFile)
	if err != nil {
		return err
	}
	defer release()
	store, err := owner.OpenStore(cfg.StateFile, cfg.GuildID, time.Now())
	if err != nil {
		return err
	}
	trigger := make(chan struct{}, 1)
	fatal := make(chan error, 1)
	var controller *owner.Controller
	request := func(channelID string, lookup bool) {
		if err := controller.Request(channelID, lookup); err != nil && ctx.Err() == nil {
			logger.Error("owner_enqueue_failed", "channel_id", channelID, "error", err)
		}
	}
	api, err := discordapi.NewOwner(cfg.Token, cfg.GuildID, discordapi.OwnerHandlers{
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
		ChannelCreate: func(ch owner.Channel) {
			if controller.Eligible(&ch) {
				request(ch.ID, true)
			}
		},
		ChannelUpdate: func(ch owner.Channel) {
			if controller.Eligible(&ch) {
				request(ch.ID, false)
			}
		},
		ChannelDelete: func(ch owner.Channel) {
			for _, id := range cfg.CategoryIDs {
				if ch.ID == id {
					logger.Error("managed_category_deleted", "category_id", id, "message", "Category permissions allow its deletion; update configuration. Children are now uncategorized.")
				}
			}
			if _, tracked := store.Get(ch.ID); tracked {
				request(ch.ID, false)
			}
		},
		Audit: func(entry owner.AuditEntry) {
			if controller.RememberAudit(entry) {
				request(entry.TargetID, false)
			}
		},
	}, logger)
	if err != nil {
		return fmt.Errorf("initialize Discord client: %w", err)
	}
	api.RequestTimeout = cfg.RequestTimeout
	var background sync.WaitGroup
	defer func() {
		cancel()
		if controller != nil {
			controller.Close()
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
	warnings, err := api.Validate(ctx, cfg.CategoryIDs)
	if err != nil {
		return fmt.Errorf("owner permission preflight: %w", err)
	}
	for _, warning := range warnings {
		logger.Warn("permission_setup", "message", warning)
	}
	controller, err = owner.NewController(owner.Config{
		GuildID: cfg.GuildID, BotID: botID, CategoryIDs: cfg.CategoryIDs,
		Workers: cfg.Workers, QueueCapacity: cfg.QueueCapacity,
	}, store, api, logger)
	if err != nil {
		return err
	}
	controller.Start(ctx)
	legacyWarned := make(map[string]bool)
	scan := func(ctx context.Context) {
		ids := make(map[string]bool)
		for _, id := range store.IDs() {
			ids[id] = true
		}
		channels, err := api.ListGuildChannels(ctx)
		if err != nil && ctx.Err() == nil {
			logger.Error("owner_channel_scan_failed", "error", err)
		}
		for i := range channels {
			ch := &channels[i]
			if controller.Eligible(ch) {
				ids[ch.ID] = true
			} else if controller.InScope(ch) && !legacyWarned[ch.ID] {
				legacyWarned[ch.ID] = true
				logger.Warn("existing_channel_not_managed", "channel_id", ch.ID, "message", "This channel predates activation; protect its permissions manually.")
			}
		}
		ordered := make([]string, 0, len(ids))
		for id := range ids {
			ordered = append(ordered, id)
		}
		sort.Strings(ordered)
		parallel(ctx, cfg.Workers, ordered, func(ctx context.Context, id string) {
			if _, err := controller.Reconcile(ctx, id, true); err != nil && ctx.Err() == nil {
				logger.Error("owner_reconcile_failed", "channel_id", id, "error", err)
			}
		})
	}
	background.Add(1)
	go func() { defer background.Done(); periodic(ctx, cfg.ReconcileInterval, trigger, scan) }()
	logger.Info("owner_starting", "guild_id", cfg.GuildID, "category_ids", cfg.CategoryIDs,
		"activated_at", time.UnixMilli(store.ActivatedAt()).UTC().Format(time.RFC3339), "tracked_channels", len(store.IDs()))
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
