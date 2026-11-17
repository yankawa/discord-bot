package discordapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
)

func TestLinksValidateGuildMembershipAndConfiguredChannels(t *testing.T) {
	const base = discord.PermissionViewChannel | discord.PermissionReadMessageHistory | discord.PermissionEmbedLinks
	cases := []struct {
		name         string
		channels     []string
		guildStatus  int
		memberStatus int
		channelGuild string
		channelType  int
		channelFlags int
		permissions  discord.Permissions
		parentAllow  discord.Permissions
		parentDeny   discord.Permissions
		wantError    string
	}{
		{name: "all channels checks membership", permissions: 0},
		{name: "wrong or uninvited guild", guildStatus: 403, wantError: "check GUILD_ID and invite this bot"},
		{name: "bot not a guild member", memberStatus: 404, wantError: "bot membership"},
		{name: "text allowed", channels: []string{"2"}, permissions: base | discord.PermissionSendMessages},
		{name: "foreign channel", channels: []string{"2"}, channelGuild: "99", permissions: base | discord.PermissionSendMessages, wantError: "does not belong"},
		{name: "category rejected", channels: []string{"2"}, channelType: 4, permissions: base | discord.PermissionSendMessages, wantError: "contains category"},
		{name: "forum container rejected", channels: []string{"2"}, channelType: 15, permissions: base | discord.PermissionSendMessages, wantError: "exact post thread IDs"},
		{name: "obfuscated rejected", channels: []string{"2"}, channelFlags: 1 << 17, permissions: base | discord.PermissionSendMessages, wantError: "obfuscated"},
		{name: "missing send permission", channels: []string{"2"}, permissions: base, wantError: "Send Messages"},
		{name: "thread uses parent permission", channels: []string{"2"}, channelType: 11, permissions: base, parentAllow: discord.PermissionSendMessagesInThreads},
		{name: "thread parent denies embedding", channels: []string{"2"}, channelType: 11, permissions: base | discord.PermissionSendMessagesInThreads, parentDeny: discord.PermissionEmbedLinks, wantError: "Embed Links"},
		{name: "thread ordinary send is insufficient", channels: []string{"2"}, channelType: 11, permissions: base | discord.PermissionSendMessages, wantError: "Send Messages in Threads"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := &Links{guildID: 1, client: mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/users/@me":
					writeJSON(t, w, map[string]any{"id": "8", "bot": true})
				case "/guilds/1":
					if tc.guildStatus != 0 {
						w.WriteHeader(tc.guildStatus)
						_, _ = io.WriteString(w, `{"code":50001,"message":"Missing Access"}`)
						return
					}
					writeJSON(t, w, map[string]any{"id": "1", "owner_id": "99", "roles": []any{map[string]any{"id": "1", "permissions": "0"}, map[string]any{"id": "4", "permissions": fmt.Sprint(uint64(tc.permissions))}}})
				case "/guilds/1/members/8":
					if tc.memberStatus != 0 {
						w.WriteHeader(tc.memberStatus)
						_, _ = io.WriteString(w, `{"code":10007,"message":"Unknown Member"}`)
						return
					}
					writeJSON(t, w, map[string]any{"user": map[string]any{"id": "8", "bot": true}, "roles": []string{"4"}})
				case "/channels/2":
					guildID := tc.channelGuild
					if guildID == "" {
						guildID = "1"
					}
					writeJSON(t, w, map[string]any{"id": "2", "guild_id": guildID, "parent_id": "6", "type": tc.channelType, "flags": tc.channelFlags, "permission_overwrites": []any{}})
				case "/channels/6":
					// A thread inherits this parent ACL. Its own overwrite list
					// is intentionally empty in Discord's response.
					writeJSON(t, w, map[string]any{"id": "6", "guild_id": "1", "type": 15, "permission_overwrites": []any{map[string]any{"id": "1", "type": 0, "allow": fmt.Sprint(uint64(tc.parentAllow)), "deny": fmt.Sprint(uint64(tc.parentDeny))}}})
				default:
					t.Errorf("unexpected validation request: %s", r.URL)
					w.WriteHeader(500)
				}
			})}
			err := l.Validate(context.Background(), tc.channels)
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected error containing %q, got %v", tc.wantError, err)
			}
		})
	}
}
