package discordapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astrapath-dev/discord-bot/internal/links"
	"github.com/astrapath-dev/discord-bot/internal/owner"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

func mockClient(t *testing.T, handler http.HandlerFunc) *bot.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := rest.NewClient("test-token", rest.WithURL(server.URL), rest.WithHTTPClient(server.Client()), rest.WithLogger(logger), rest.WithRateLimiter(rest.NewNoopRateLimiter()))
	t.Cleanup(func() { r.Close(context.Background()) })
	return &bot.Client{Rest: rest.New(r), Logger: logger}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func TestLinkReplyRequestUsesExactNonceReferenceAndNoMentions(t *testing.T) {
	var calls atomic.Int32
	client := mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/channels/2/messages" || r.Method != http.MethodPost {
			t.Errorf("unexpected route: %s %s", r.Method, r.URL)
		}
		var payload struct {
			Content  string `json:"content"`
			Nonce    string `json:"nonce"`
			Enforce  bool   `json:"enforce_nonce"`
			Mentions struct {
				Parse []string `json:"parse"`
				Roles []string `json:"roles"`
				Users []string `json:"users"`
				Reply bool     `json:"replied_user"`
			} `json:"allowed_mentions"`
			Reference struct {
				MessageID string `json:"message_id"`
				ChannelID string `json:"channel_id"`
				GuildID   string `json:"guild_id"`
				Fail      bool   `json:"fail_if_not_exists"`
			} `json:"message_reference"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Content != "https://fxtwitter.com/user/status/4" || payload.Nonce != "3-1" || !payload.Enforce {
			t.Errorf("content or idempotency fields lost: %+v", payload)
		}
		if payload.Mentions.Parse == nil || payload.Mentions.Roles == nil || payload.Mentions.Users == nil || len(payload.Mentions.Parse)+len(payload.Mentions.Roles)+len(payload.Mentions.Users) != 0 || payload.Mentions.Reply {
			t.Errorf("mentions were not explicitly disabled: %+v", payload.Mentions)
		}
		if payload.Reference.MessageID != "3" || payload.Reference.ChannelID != "2" || payload.Reference.GuildID != "1" || !payload.Reference.Fail {
			t.Errorf("incorrect source reference: %+v", payload.Reference)
		}
		writeJSON(t, w, map[string]any{"id": "5", "channel_id": "2"})
	})
	l := &Links{client: client, guildID: 1}
	id, err := l.CreateReply(context.Background(), links.Message{ID: "3", ChannelID: "2", GuildID: "1"}, "https://fxtwitter.com/user/status/4", "3-1")
	if err != nil || id != "5" || calls.Load() != 1 {
		t.Fatalf("id=%s err=%v calls=%d", id, err, calls.Load())
	}
	_, err = l.CreateReply(context.Background(), links.Message{ID: "3", ChannelID: "2", GuildID: "6"}, "link", "3")
	if err == nil || calls.Load() != 1 {
		t.Fatal("outside-guild reply reached Discord")
	}
}

func TestLinkGetMessageFillsGuildWithoutLosingBotAndWebhookFlags(t *testing.T) {
	l := &Links{guildID: 1, client: mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"id": "3", "channel_id": "2", "content": "hello", "author": map[string]any{"id": "8", "bot": true}, "webhook_id": "9"})
	})}
	m, err := l.GetMessage(context.Background(), "2", "3")
	if err != nil || m == nil || m.GuildID != "1" || m.Content != "hello" || !m.Bot || !m.Webhook {
		t.Fatalf("message=%+v err=%v", m, err)
	}
}

func TestMessageFailuresPreserveAccessErrors(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			l := &Links{guildID: 1, client: mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"code":10008,"message":"unavailable"}`)
			})}
			message, getErr := l.GetMessage(context.Background(), "2", "3")
			found, editErr := l.EditReply(context.Background(), "2", "3", "content")
			deleteErr := l.DeleteReply(context.Background(), "2", "3")
			if message != nil || found {
				t.Fatal("failed request returned an object")
			}
			if status == http.StatusNotFound {
				if getErr != nil || editErr != nil || deleteErr != nil {
					t.Fatalf("404 should be absence: %v %v %v", getErr, editErr, deleteErr)
				}
			} else if getErr == nil || editErr == nil || deleteErr == nil {
				t.Fatalf("403 must propagate: %v %v %v", getErr, editErr, deleteErr)
			}
		})
	}
}

func TestLinkEditDisablesMentions(t *testing.T) {
	l := &Links{guildID: 1, client: mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method=%s", r.Method)
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		var mentions discord.AllowedMentions
		if err := json.Unmarshal(payload["allowed_mentions"], &mentions); err != nil {
			t.Error(err)
		}
		if len(mentions.Parse)+len(mentions.Roles)+len(mentions.Users) != 0 || mentions.RepliedUser {
			t.Errorf("unsafe mentions: %+v", mentions)
		}
		if _, ok := payload["flags"]; ok {
			t.Error("editing content should not alter unrelated message flags")
		}
		writeJSON(t, w, map[string]any{"id": "3", "channel_id": "2"})
	})}
	if found, err := l.EditReply(context.Background(), "2", "3", "new content"); err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestOwnerPermissionPatchPreservesPayloadAndReturnedFlags(t *testing.T) {
	want := []owner.Overwrite{{ID: "1", Type: 0, Allow: 2048, Deny: owner.ManageChannels | owner.ManageRoles}, {ID: "8", Type: 1, Allow: owner.ManageChannels | owner.ViewChannel, Deny: owner.ManageRoles}}
	o := &Owner{guildID: 1, client: mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/channels/2" {
			t.Errorf("unexpected route: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("X-Audit-Log-Reason") == "" {
			t.Error("missing audit reason")
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if len(payload) != 1 || payload["permission_overwrites"] == nil {
			t.Errorf("permission patch touches unrelated fields: %s", payload)
		}
		var values []map[string]any
		if err := json.Unmarshal(payload["permission_overwrites"], &values); err != nil {
			t.Error(err)
		}
		if values[0]["allow"] != "2048" || values[0]["deny"] != "268435472" || values[1]["type"] != float64(1) {
			t.Errorf("incorrect serialized permission bits: %+v", values)
		}
		writeJSON(t, w, map[string]any{"id": "2", "guild_id": "1", "parent_id": "6", "type": 2, "flags": owner.ChannelObfuscated, "permission_overwrites": values})
	})}
	got, err := o.WriteOverwrites(context.Background(), "2", want, "channel owner")
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != 2 || got.Flags != owner.ChannelObfuscated || got.ParentID != "6" || !reflect.DeepEqual(got.Overwrites, want) {
		t.Fatalf("incorrect returned snapshot: %+v", got)
	}
}

func TestOwnerFetchOnly404MeansMissing(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			o := &Owner{guildID: 1, client: mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"code":10003,"message":"unavailable"}`)
			})}
			got, err := o.FetchChannel(context.Background(), "2")
			if got != nil || (status == http.StatusNotFound && err != nil) || (status == http.StatusForbidden && err == nil) {
				t.Fatalf("got=%v err=%v", got, err)
			}
		})
	}
}

func TestOwnerAuditRequestsCreateFilterAndExactTarget(t *testing.T) {
	o := &Owner{guildID: 1, client: mockClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/guilds/1/audit-logs" || r.URL.Query().Get("action_type") != "10" || r.URL.Query().Get("limit") != "100" {
			t.Errorf("unexpected audit query: %s", r.URL)
		}
		writeJSON(t, w, map[string]any{"audit_log_entries": []any{map[string]any{"id": "11", "action_type": 10, "target_id": "99", "user_id": "7"}, map[string]any{"id": "10", "action_type": 10, "target_id": "2", "user_id": "8"}}})
	})}
	entry, err := o.FindAudit(context.Background(), "2")
	if err != nil || entry == nil || entry.ExecutorID != "8" || entry.ID != "10" {
		t.Fatalf("entry=%+v err=%v", entry, err)
	}
}

func TestRequestsHonorContextDeadline(t *testing.T) {
	l := &Links{guildID: 1, RequestTimeout: 50 * time.Millisecond, client: mockClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })}
	_, err := l.GetMessage(context.Background(), "2", "3")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestInvalidIDsCannotReachREST(t *testing.T) {
	var calls atomic.Int32
	l := &Links{guildID: 1, client: mockClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) })}
	for _, id := range []string{"0", "-1", "1/../../guilds", "18446744073709551616", "01"} {
		if _, err := l.GetMessage(context.Background(), id, "3"); err == nil {
			t.Errorf("accepted %q", id)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid ID reached REST")
	}
}

func TestPermissionsApplyRoleAllowAndMemberDenyInDiscordOrder(t *testing.T) {
	member := discord.Member{User: discord.User{ID: 8}, RoleIDs: []snowflake.ID{4, 5}}
	overwrites := []owner.Overwrite{{ID: "1", Type: 0, Deny: owner.ManageChannels}, {ID: "4", Type: 0, Deny: owner.ManageChannels}, {ID: "5", Type: 0, Allow: owner.ManageChannels}, {ID: "8", Type: 1, Deny: owner.ManageRoles}}
	got := permissionsInChannel(owner.ManageChannels|owner.ManageRoles|owner.ViewChannel, 1, member, overwrites)
	if got != owner.ManageChannels|owner.ViewChannel {
		t.Fatalf("permissions=%d", got)
	}
	admin := permissionsInChannel(uint64(discord.PermissionAdministrator), 1, member, overwrites)
	if admin&owner.ManageRoles == 0 {
		t.Fatal("administrator unexpectedly denied")
	}
}

func TestOwnerValidateChecksCurrentGuildAndCategoryPermissions(t *testing.T) {
	for _, hasAudit := range []bool{true, false} {
		t.Run(fmt.Sprint(hasAudit), func(t *testing.T) {
			permissions := owner.ViewChannel | owner.ManageChannels | owner.ManageRoles | owner.Connect
			if hasAudit {
				permissions |= uint64(discord.PermissionViewAuditLog)
			}
			o := &Owner{guildID: 1, client: mockClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/users/@me":
					writeJSON(t, w, map[string]any{"id": "8", "bot": true})
				case "/guilds/1":
					writeJSON(t, w, map[string]any{"id": "1", "owner_id": "99", "roles": []any{map[string]any{"id": "1", "permissions": "1024"}, map[string]any{"id": "4", "permissions": fmt.Sprint(permissions)}}})
				case "/guilds/1/members/8":
					writeJSON(t, w, map[string]any{"user": map[string]any{"id": "8", "bot": true}, "roles": []string{"4"}})
				case "/channels/6":
					writeJSON(t, w, map[string]any{"id": "6", "guild_id": "1", "type": 4, "permission_overwrites": []any{}})
				default:
					t.Errorf("unexpected route: %s", r.URL.Path)
					w.WriteHeader(500)
				}
			})}
			warnings, err := o.Validate(context.Background(), []string{"6"})
			if hasAudit {
				if err != nil || len(warnings) != 1 || !strings.Contains(warnings[0], "@everyone") {
					t.Fatalf("warnings=%v err=%v", warnings, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "View Audit Log") {
				t.Fatalf("missing audit permission accepted: %v", err)
			}
		})
	}
}
