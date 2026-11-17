package discordapi

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/astrapath-dev/discord-bot/internal/owner"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

type OwnerHandlers struct {
	Ready         func(botID string)
	Fatal         func(error)
	ChannelCreate func(owner.Channel)
	ChannelUpdate func(owner.Channel)
	ChannelDelete func(owner.Channel)
	Audit         func(owner.AuditEntry)
}

// Owner is one Discord application/client. Set RequestTimeout before Open.
type Owner struct {
	client         *bot.Client
	guildID        snowflake.ID
	RequestTimeout time.Duration
}

var _ owner.API = (*Owner)(nil)

func NewOwner(token, guildID string, handlers OwnerHandlers, logger *slog.Logger) (*Owner, error) {
	id, err := parseID(guildID)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	o := &Owner{guildID: id, RequestTimeout: defaultRequestTimeout}
	channelEvent := func(channel discord.GuildChannel, callback func(owner.Channel)) {
		if callback == nil || channel.GuildID() != id {
			return
		}
		snapshot, err := ownerChannel(channel)
		if err != nil {
			logger.Error("cannot decode channel event", "channel_id", channel.ID().String(), "error", err)
			return
		}
		callback(*snapshot)
	}
	client, err := makeClient(token, logger, gateway.IntentGuilds|gateway.IntentGuildModeration, handlers.Fatal,
		bot.NewListenerFunc(func(event *events.Ready) {
			if handlers.Ready != nil {
				handlers.Ready(event.User.ID.String())
			}
		}),
		bot.NewListenerFunc(func(event *events.Resumed) {
			if handlers.Ready != nil {
				handlers.Ready(event.Client().ID().String())
			}
		}),
		bot.NewListenerFunc(func(event *events.GuildChannelCreate) { channelEvent(event.Channel, handlers.ChannelCreate) }),
		bot.NewListenerFunc(func(event *events.GuildChannelUpdate) { channelEvent(event.Channel, handlers.ChannelUpdate) }),
		bot.NewListenerFunc(func(event *events.GuildChannelDelete) { channelEvent(event.Channel, handlers.ChannelDelete) }),
		bot.NewListenerFunc(func(event *events.GuildAuditLogEntryCreate) {
			if event.GuildID == id && handlers.Audit != nil {
				handlers.Audit(ownerAudit(event.AuditLogEntry))
			}
		}),
	)
	if err != nil {
		return nil, err
	}
	o.client = client
	return o, nil
}

func (o *Owner) Open(ctx context.Context) error { return o.client.OpenGateway(ctx) }
func (o *Owner) Close(ctx context.Context)      { o.client.Close(ctx) }
func (o *Owner) BotID(ctx context.Context) (string, error) {
	return fetchBotID(ctx, o.client, o.RequestTimeout)
}

func (o *Owner) FetchChannel(ctx context.Context, channelID string) (*owner.Channel, error) {
	id, err := parseID(channelID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := requestContext(ctx, o.RequestTimeout)
	defer cancel()
	channel, err := o.client.Rest.GetChannel(id, rest.WithCtx(ctx))
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	guildChannel, ok := channel.(discord.GuildChannel)
	if !ok {
		return nil, fmt.Errorf("channel %s is not a guild channel", channelID)
	}
	return ownerChannel(guildChannel)
}

func (o *Owner) ListGuildChannels(ctx context.Context) ([]owner.Channel, error) {
	ctx, cancel := requestContext(ctx, o.RequestTimeout)
	defer cancel()
	channels, err := o.client.Rest.GetGuildChannels(o.guildID, rest.WithCtx(ctx))
	if err != nil {
		return nil, err
	}
	result := make([]owner.Channel, 0, len(channels))
	for _, channel := range channels {
		snapshot, err := ownerChannel(channel)
		if err != nil {
			return nil, fmt.Errorf("decode channel %s: %w", channel.ID(), err)
		}
		// The list endpoint can omit guild_id; its route supplies the scope.
		if snapshot.GuildID == "0" || snapshot.GuildID == "" {
			snapshot.GuildID = o.guildID.String()
		}
		result = append(result, *snapshot)
	}
	return result, nil
}

func (o *Owner) WriteOverwrites(ctx context.Context, channelID string, overwrites []owner.Overwrite, reason string) (*owner.Channel, error) {
	id, err := parseID(channelID)
	if err != nil {
		return nil, err
	}
	desired := make([]discord.PermissionOverwrite, 0, len(overwrites))
	for _, overwrite := range overwrites {
		overwriteID, err := parseID(overwrite.ID)
		if err != nil {
			return nil, err
		}
		if overwrite.Allow > math.MaxInt64 || overwrite.Deny > math.MaxInt64 {
			return nil, fmt.Errorf("permission bits exceed the Discord SDK range")
		}
		switch overwrite.Type {
		case 0:
			desired = append(desired, discord.RolePermissionOverwrite{RoleID: overwriteID, Allow: discord.Permissions(overwrite.Allow), Deny: discord.Permissions(overwrite.Deny)})
		case 1:
			desired = append(desired, discord.MemberPermissionOverwrite{UserID: overwriteID, Allow: discord.Permissions(overwrite.Allow), Deny: discord.Permissions(overwrite.Deny)})
		default:
			return nil, fmt.Errorf("unknown overwrite type %d", overwrite.Type)
		}
	}
	ctx, cancel := requestContext(ctx, o.RequestTimeout)
	defer cancel()
	// This update serializes ONLY permission_overwrites. The endpoint accepts
	// that field for every supported non-thread guild channel type.
	updated, err := o.client.Rest.UpdateChannel(id, discord.GuildTextChannelUpdate{PermissionOverwrites: &desired}, rest.WithCtx(ctx), rest.WithReason(reason))
	if err != nil {
		return nil, err
	}
	channel, ok := updated.(discord.GuildChannel)
	if !ok {
		return nil, fmt.Errorf("updated channel %s is not a guild channel", channelID)
	}
	return ownerChannel(channel)
}

func (o *Owner) FindAudit(ctx context.Context, channelID string) (*owner.AuditEntry, error) {
	return owner.LookupCreateEntry(ctx, func(ctx context.Context, before string, limit int) ([]owner.AuditEntry, error) {
		var beforeID snowflake.ID
		if before != "" {
			var err error
			beforeID, err = parseID(before)
			if err != nil {
				return nil, err
			}
		}
		ctx, cancel := requestContext(ctx, o.RequestTimeout)
		defer cancel()
		page, err := o.client.Rest.GetAuditLog(o.guildID, 0, discord.AuditLogEvent(owner.ChannelCreateAction), beforeID, 0, limit, rest.WithCtx(ctx))
		if err != nil {
			return nil, err
		}
		if page == nil {
			return nil, fmt.Errorf("Discord returned an empty audit response")
		}
		entries := make([]owner.AuditEntry, 0, len(page.AuditLogEntries))
		for _, entry := range page.AuditLogEntries {
			entries = append(entries, ownerAudit(entry))
		}
		return entries, nil
	}, channelID, 5)
}

func ownerAudit(entry discord.AuditLogEntry) owner.AuditEntry {
	result := owner.AuditEntry{ID: entry.ID.String(), Action: int(entry.ActionType)}
	if entry.TargetID != nil {
		result.TargetID = entry.TargetID.String()
	}
	if entry.UserID != 0 {
		result.ExecutorID = entry.UserID.String()
	}
	return result
}

func ownerChannel(channel discord.GuildChannel) (*owner.Channel, error) {
	result := &owner.Channel{
		ID: channel.ID().String(), GuildID: channel.GuildID().String(), Type: int(channel.Type()),
		CreatedAt: channel.CreatedAt().UnixMilli(), Flags: uint64(channel.Flags()),
		Overwrites: make([]owner.Overwrite, 0, len(channel.PermissionOverwrites())),
	}
	if channel.ParentID() != nil {
		result.ParentID = channel.ParentID().String()
	}
	for _, overwrite := range channel.PermissionOverwrites() {
		item := owner.Overwrite{ID: overwrite.ID().String(), Type: int(overwrite.Type())}
		var allow, deny discord.Permissions
		switch value := overwrite.(type) {
		case discord.RolePermissionOverwrite:
			allow, deny = value.Allow, value.Deny
		case discord.MemberPermissionOverwrite:
			allow, deny = value.Allow, value.Deny
		case discord.UnknownPermissionOverwrite:
			return nil, fmt.Errorf("unsupported permission overwrite type %d", value.Type())
		default:
			return nil, fmt.Errorf("unsupported permission overwrite representation %T", value)
		}
		if allow < 0 || deny < 0 {
			return nil, fmt.Errorf("negative permission bits")
		}
		item.Allow, item.Deny = uint64(allow), uint64(deny)
		result.Overwrites = append(result.Overwrites, item)
	}
	return result, nil
}
