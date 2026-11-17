package discordapi

import (
	"context"
	"fmt"
	"time"

	"github.com/astrapath-dev/discord-bot/internal/owner"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

// Validate checks the bot's current guild/category permissions without mutating
// Discord. It uses fresh REST objects, so it also works before Gateway READY.
func (o *Owner) Validate(ctx context.Context, categoryIDs []string) ([]string, error) {
	botIDString, err := o.BotID(ctx)
	if err != nil {
		return nil, err
	}
	botID, _ := parseID(botIDString)
	requestCtx, cancel := requestContext(ctx, o.RequestTimeout)
	guild, err := o.client.Rest.GetGuild(o.guildID, false, rest.WithCtx(requestCtx))
	cancel()
	if err != nil {
		return nil, fmt.Errorf("read target guild: %w", err)
	}
	if guild == nil || guild.ID != o.guildID {
		return nil, fmt.Errorf("Discord returned an inconsistent guild identity")
	}
	requestCtx, cancel = requestContext(ctx, o.RequestTimeout)
	member, err := o.client.Rest.GetMember(o.guildID, botID, rest.WithCtx(requestCtx))
	cancel()
	if err != nil {
		return nil, fmt.Errorf("read bot guild membership: %w", err)
	}
	if member == nil || member.User.ID != botID {
		return nil, fmt.Errorf("Discord returned an inconsistent bot membership")
	}
	global, everyone, err := guildPermissions(*guild, *member)
	if err != nil {
		return nil, err
	}
	if global&uint64(discord.PermissionViewAuditLog) == 0 {
		return nil, fmt.Errorf("bot requires View Audit Log in guild %s", o.guildID)
	}
	if len(categoryIDs) == 0 {
		return nil, fmt.Errorf("at least one target category is required")
	}
	warnings := make([]string, 0)
	for _, categoryID := range categoryIDs {
		category, err := o.FetchChannel(ctx, categoryID)
		if err != nil {
			return nil, fmt.Errorf("read target category %s: %w", categoryID, err)
		}
		if category == nil || category.GuildID != o.guildID.String() || category.Type != int(discord.ChannelTypeGuildCategory) {
			return nil, fmt.Errorf("%s is not an accessible category in the configured guild", categoryID)
		}
		if category.Flags&owner.ChannelObfuscated != 0 {
			return nil, fmt.Errorf("category %s is obfuscated: grant the bot View Channel", categoryID)
		}
		permissions := permissionsInChannel(global, o.guildID, *member, category.Overwrites)
		// Connect is included because the same category can accept voice/stage
		// channels, and the bot must be able to grant it to their creator.
		required := owner.ViewChannel | owner.ManageChannels | owner.ManageRoles | owner.Connect
		if permissions&required != required {
			return nil, fmt.Errorf("bot requires View Channel, Manage Channels, Manage Roles and Connect in category %s (missing bits %d)", categoryID, required&^permissions)
		}
		everyoneInCategory := everyone
		for _, overwrite := range category.Overwrites {
			if overwrite.Type == 0 && overwrite.ID == o.guildID.String() {
				everyoneInCategory = (everyoneInCategory &^ overwrite.Deny) | overwrite.Allow
			}
		}
		if everyoneInCategory&owner.ManageChannels == 0 && everyoneInCategory&uint64(discord.PermissionAdministrator) == 0 {
			warnings = append(warnings, fmt.Sprintf("category %s does not allow @everyone to Manage Channels; normal members cannot use Discord's native Create Channel action", categoryID))
		}
	}
	return warnings, nil
}

func guildPermissions(guild discord.RestGuild, member discord.Member) (global, everyone uint64, err error) {
	roles := make(map[snowflake.ID]struct{}, len(member.RoleIDs)+1)
	roles[guild.ID] = struct{}{}
	for _, id := range member.RoleIDs {
		roles[id] = struct{}{}
	}
	foundEveryone := false
	for _, role := range guild.Roles {
		if role.Permissions < 0 {
			return 0, 0, fmt.Errorf("guild role contains negative permission bits")
		}
		if role.ID == guild.ID {
			everyone = uint64(role.Permissions)
			foundEveryone = true
		}
		if _, ok := roles[role.ID]; ok {
			global |= uint64(role.Permissions)
		}
	}
	if !foundEveryone {
		return 0, 0, fmt.Errorf("guild response lacks the @everyone role")
	}
	if guild.OwnerID == member.User.ID || global&uint64(discord.PermissionAdministrator) != 0 {
		return ^uint64(0), everyone, nil
	}
	if member.CommunicationDisabledUntil != nil && member.CommunicationDisabledUntil.After(time.Now()) {
		global &= uint64(discord.PermissionViewChannel | discord.PermissionReadMessageHistory)
	}
	return global, everyone, nil
}

func permissionsInChannel(global uint64, guildID snowflake.ID, member discord.Member, overwrites []owner.Overwrite) uint64 {
	if global&uint64(discord.PermissionAdministrator) != 0 {
		return ^uint64(0)
	}
	permissions := global
	roleIDs := make(map[string]struct{}, len(member.RoleIDs))
	for _, id := range member.RoleIDs {
		if id != guildID {
			roleIDs[id.String()] = struct{}{}
		}
	}
	var roleAllow, roleDeny uint64
	var memberAllow, memberDeny uint64
	for _, overwrite := range overwrites {
		if overwrite.Type == 0 && overwrite.ID == guildID.String() {
			permissions = (permissions &^ overwrite.Deny) | overwrite.Allow
		} else if overwrite.Type == 0 {
			if _, ok := roleIDs[overwrite.ID]; ok {
				roleAllow |= overwrite.Allow
				roleDeny |= overwrite.Deny
			}
		} else if overwrite.Type == 1 && overwrite.ID == member.User.ID.String() {
			memberAllow, memberDeny = overwrite.Allow, overwrite.Deny
		}
	}
	permissions = (permissions &^ roleDeny) | roleAllow
	permissions = (permissions &^ memberDeny) | memberAllow
	if member.CommunicationDisabledUntil != nil && member.CommunicationDisabledUntil.After(time.Now()) {
		permissions &= uint64(discord.PermissionViewChannel | discord.PermissionReadMessageHistory)
	}
	return permissions
}
