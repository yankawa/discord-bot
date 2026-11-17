package discordapi

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/astrapath-dev/discord-bot/internal/links"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

type LinkHandlers struct {
	Ready         func(botID string)
	Fatal         func(error)
	MessageCreate func(links.Message)
	MessageUpdate func(channelID, messageID string)
	MessageDelete func(channelID, messageID string)
}

// Links is independent of Owner, with a separate token and Gateway session.
// Set RequestTimeout before Open; changing it during requests is unsupported.
type Links struct {
	client         *bot.Client
	guildID        snowflake.ID
	RequestTimeout time.Duration
}

var _ links.API = (*Links)(nil)

func NewLinks(token, guildID string, handlers LinkHandlers, logger *slog.Logger) (*Links, error) {
	id, err := parseID(guildID)
	if err != nil {
		return nil, err
	}
	l := &Links{guildID: id, RequestTimeout: defaultRequestTimeout}
	client, err := makeClient(token, logger, gateway.IntentGuilds|gateway.IntentGuildMessages|gateway.IntentMessageContent, handlers.Fatal,
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
		bot.NewListenerFunc(func(event *events.MessageCreate) {
			if event.GuildID != nil && *event.GuildID == id && handlers.MessageCreate != nil {
				handlers.MessageCreate(linkMessage(event.Message, id))
			}
		}),
		bot.NewListenerFunc(func(event *events.MessageUpdate) {
			if event.GuildID != nil && *event.GuildID == id && handlers.MessageUpdate != nil {
				// Discord may send partial updates, including embed-only changes.
				// The core re-fetches tracked source messages instead of treating
				// omitted content as an instruction to delete a reply.
				handlers.MessageUpdate(event.ChannelID.String(), event.MessageID.String())
			}
		}),
		bot.NewListenerFunc(func(event *events.MessageDelete) {
			if event.GuildID != nil && *event.GuildID == id && handlers.MessageDelete != nil {
				// disgo expands MESSAGE_DELETE_BULK into these events as well.
				handlers.MessageDelete(event.ChannelID.String(), event.MessageID.String())
			}
		}),
	)
	if err != nil {
		return nil, err
	}
	l.client = client
	return l, nil
}

func (l *Links) Open(ctx context.Context) error { return l.client.OpenGateway(ctx) }
func (l *Links) Close(ctx context.Context)      { l.client.Close(ctx) }
func (l *Links) BotID(ctx context.Context) (string, error) {
	return fetchBotID(ctx, l.client, l.RequestTimeout)
}

func (l *Links) GetMessage(ctx context.Context, channelID, messageID string) (*links.Message, error) {
	channel, err := parseID(channelID)
	if err != nil {
		return nil, err
	}
	message, err := parseID(messageID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := requestContext(ctx, l.RequestTimeout)
	defer cancel()
	result, err := l.client.Rest.GetMessage(channel, message, rest.WithCtx(ctx))
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if result == nil || result.ID != message || result.ChannelID != channel {
		return nil, fmt.Errorf("Discord returned an inconsistent message identity")
	}
	// REST message objects may omit guild_id. The core only calls this for
	// records/events already scoped to this configured guild.
	converted := linkMessage(*result, l.guildID)
	return &converted, nil
}

func (l *Links) CreateReply(ctx context.Context, source links.Message, content, nonce string) (string, error) {
	channelID, err := parseID(source.ChannelID)
	if err != nil {
		return "", err
	}
	messageID, err := parseID(source.ID)
	if err != nil {
		return "", err
	}
	if source.GuildID != l.guildID.String() {
		return "", fmt.Errorf("reply source is outside the configured guild")
	}
	if len(nonce) == 0 || len(nonce) > 25 {
		return "", fmt.Errorf("reply nonce must contain 1 to 25 bytes")
	}
	ctx, cancel := requestContext(ctx, l.RequestTimeout)
	defer cancel()
	created, err := l.client.Rest.CreateMessage(channelID, discord.MessageCreate{
		Content: content, Nonce: nonce, EnforceNonce: true, AllowedMentions: noMentions(),
		MessageReference: &discord.MessageReference{
			MessageID: &messageID, ChannelID: &channelID, GuildID: &l.guildID,
			// If the source was deleted between read and send, do not leave an
			// unreferenced repost behind. The core retries/reconciles safely.
			FailIfNotExists: true,
		},
	}, rest.WithCtx(ctx))
	if err != nil {
		return "", err
	}
	if created == nil || created.ID == 0 {
		return "", fmt.Errorf("Discord returned no reply ID")
	}
	return created.ID.String(), nil
}

func (l *Links) EditReply(ctx context.Context, channelID, replyID, content string) (bool, error) {
	channel, err := parseID(channelID)
	if err != nil {
		return false, err
	}
	reply, err := parseID(replyID)
	if err != nil {
		return false, err
	}
	ctx, cancel := requestContext(ctx, l.RequestTimeout)
	defer cancel()
	_, err = l.client.Rest.UpdateMessage(channel, reply, discord.MessageUpdate{Content: &content, AllowedMentions: noMentions()}, rest.WithCtx(ctx))
	if isNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

func (l *Links) DeleteReply(ctx context.Context, channelID, replyID string) error {
	channel, err := parseID(channelID)
	if err != nil {
		return err
	}
	reply, err := parseID(replyID)
	if err != nil {
		return err
	}
	ctx, cancel := requestContext(ctx, l.RequestTimeout)
	defer cancel()
	err = l.client.Rest.DeleteMessage(channel, reply, rest.WithCtx(ctx))
	if isNotFound(err) {
		return nil
	}
	return err
}

func linkMessage(message discord.Message, fallbackGuild snowflake.ID) links.Message {
	guildID := fallbackGuild
	if message.GuildID != nil {
		guildID = *message.GuildID
	}
	return links.Message{
		ID: message.ID.String(), ChannelID: message.ChannelID.String(), GuildID: guildID.String(),
		Content: message.Content, Bot: message.Author.Bot, Webhook: message.WebhookID != nil,
	}
}
