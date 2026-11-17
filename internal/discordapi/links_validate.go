package discordapi

import (
	"context"
	"fmt"

	"github.com/astrapath-dev/discord-bot/internal/owner"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
)

// Validate fails startup for an inaccessible guild or unusable explicitly
// configured channels. With an empty allowlist it checks guild membership;
// permissions in individual channels are then enforced by Discord at runtime.
func (l *Links) Validate(ctx context.Context, channelIDs []string) error {
	botIDString, err := l.BotID(ctx)
	if err != nil {
		return err
	}
	botID, _ := parseID(botIDString)
	requestCtx, cancel := requestContext(ctx, l.RequestTimeout)
	guild, err := l.client.Rest.GetGuild(l.guildID, false, rest.WithCtx(requestCtx))
	cancel()
	if err != nil {
		return fmt.Errorf("read configured guild %s; check GUILD_ID and invite this bot: %w", l.guildID, err)
	}
	if guild == nil || guild.ID != l.guildID {
		return fmt.Errorf("Discord returned an inconsistent guild identity")
	}
	requestCtx, cancel = requestContext(ctx, l.RequestTimeout)
	member, err := l.client.Rest.GetMember(l.guildID, botID, rest.WithCtx(requestCtx))
	cancel()
	if err != nil {
		return fmt.Errorf("read bot membership in guild %s; check GUILD_ID and invite this bot: %w", l.guildID, err)
	}
	if member == nil || member.User.ID != botID {
		return fmt.Errorf("Discord returned an inconsistent bot membership")
	}
	if len(channelIDs) == 0 {
		return nil
	}
	global, _, err := guildPermissions(*guild, *member)
	if err != nil {
		return err
	}
	for _, channelID := range channelIDs {
		channel, err := l.validationChannel(ctx, channelID)
		if err != nil {
			return err
		}
		aclChannel := channel
		sendPermission := discord.PermissionSendMessages
		sendName := "Send Messages"
		switch discord.ChannelType(channel.Type) {
		case discord.ChannelTypeGuildText, discord.ChannelTypeGuildNews, discord.ChannelTypeGuildVoice, discord.ChannelTypeGuildStageVoice:
		case discord.ChannelTypeGuildNewsThread, discord.ChannelTypeGuildPublicThread, discord.ChannelTypeGuildPrivateThread:
			if channel.ParentID == "" {
				return fmt.Errorf("thread %s has no parent channel", channelID)
			}
			aclChannel, err = l.validationChannel(ctx, channel.ParentID)
			if err != nil {
				return fmt.Errorf("read permissions for thread %s: %w", channelID, err)
			}
			switch discord.ChannelType(aclChannel.Type) {
			case discord.ChannelTypeGuildText, discord.ChannelTypeGuildNews, discord.ChannelTypeGuildForum, discord.ChannelTypeGuildMedia:
			default:
				return fmt.Errorf("thread %s has an unsupported parent channel type", channelID)
			}
			sendPermission, sendName = discord.PermissionSendMessagesInThreads, "Send Messages in Threads"
		case discord.ChannelTypeGuildCategory:
			return fmt.Errorf("CHANNEL_IDS contains category %s; list exact message channel or thread IDs", channelID)
		case discord.ChannelTypeGuildForum, discord.ChannelTypeGuildMedia:
			return fmt.Errorf("CHANNEL_IDS contains forum/media container %s; list exact post thread IDs, or leave CHANNEL_IDS empty", channelID)
		default:
			return fmt.Errorf("channel %s has unsupported message channel type %d", channelID, channel.Type)
		}
		permissions := permissionsInChannel(global, l.guildID, *member, aclChannel.Overwrites)
		required := uint64(discord.PermissionViewChannel | discord.PermissionReadMessageHistory | discord.PermissionEmbedLinks | sendPermission)
		if permissions&required != required {
			return fmt.Errorf("bot requires View Channel, Read Message History, Embed Links and %s in channel %s (missing bits %d)", sendName, channelID, required&^permissions)
		}
	}
	return nil
}

func (l *Links) validationChannel(ctx context.Context, channelID string) (*owner.Channel, error) {
	id, err := parseID(channelID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := requestContext(ctx, l.RequestTimeout)
	defer cancel()
	channel, err := l.client.Rest.GetChannel(id, rest.WithCtx(ctx))
	if err != nil {
		return nil, fmt.Errorf("read configured channel %s; check its ID and bot access: %w", channelID, err)
	}
	guildChannel, ok := channel.(discord.GuildChannel)
	if !ok || guildChannel.ID() != id || guildChannel.GuildID() != l.guildID {
		return nil, fmt.Errorf("channel %s does not belong to the configured guild", channelID)
	}
	if guildChannel.Flags().Has(discord.ChannelFlagObfuscated) {
		return nil, fmt.Errorf("channel %s is obfuscated: grant the bot View Channel", channelID)
	}
	return ownerChannel(guildChannel)
}
