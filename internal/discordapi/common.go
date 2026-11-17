// Package discordapi adapts Discord's REST API and Gateway to the bot cores.
package discordapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/astrapath-dev/discord-bot/internal/owner"
	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

const defaultRequestTimeout = 20 * time.Second

func parseID(value string) (snowflake.ID, error) {
	if !owner.ValidID(value) {
		return 0, fmt.Errorf("invalid Discord snowflake %q", value)
	}
	n, _ := strconv.ParseUint(value, 10, 64)
	return snowflake.ID(n), nil
}

func requestContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// Only an actual HTTP 404 means absence. A 403, timeout, or server error must
// never cause the caller to discard durable state.
func isNotFound(err error) bool {
	var restErr *rest.Error
	return errors.As(err, &restErr) && restErr.Response != nil && restErr.Response.StatusCode == http.StatusNotFound
}

func noMentions() *discord.AllowedMentions {
	return &discord.AllowedMentions{
		Parse: []discord.AllowedMentionType{}, Roles: []snowflake.ID{}, Users: []snowflake.ID{}, RepliedUser: false,
	}
}

func makeClient(token string, logger *slog.Logger, intents gateway.Intents, fatal func(error), listeners ...bot.EventListener) (*bot.Client, error) {
	if logger == nil {
		logger = slog.Default()
	}
	client, err := disgo.New(token,
		bot.WithLogger(logger),
		bot.WithRestClientConfigOpts(rest.WithHTTPClient(&http.Client{Timeout: defaultRequestTimeout})),
		bot.WithRestConfigOpts(rest.WithDefaultAllowedMentions(*noMentions())),
		// No guild/member/message history caches are needed. Events contain their
		// own IDs; reconciliation deliberately reads the fresh REST state.
		bot.WithCacheConfigOpts(
			cache.WithCaches(),
			cache.WithMemberCachePolicy(cache.PolicyNone[discord.Member]),
			cache.WithMessageCachePolicy(cache.PolicyNone[discord.Message]),
		),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(intents),
			gateway.WithCloseHandler(func(_ gateway.Gateway, err error, reconnect bool) {
				if !reconnect && err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) && fatal != nil {
					fatal(err)
				}
			}),
		),
		bot.WithEventListeners(listeners...),
	)
	if err == nil {
		// disgo.New performs one gateway URL lookup. After that bootstrap,
		// every REST call in these adapters carries a bounded request context;
		// a fixed HTTP timeout would silently cap the configured value.
		client.Rest.HTTPClient().Timeout = 0
	}
	return client, err
}

func fetchBotID(ctx context.Context, client *bot.Client, timeout time.Duration) (string, error) {
	ctx, cancel := requestContext(ctx, timeout)
	defer cancel()
	user, err := client.Rest.GetCurrentUser("", rest.WithCtx(ctx))
	if err != nil {
		return "", fmt.Errorf("fetch bot identity: %w", err)
	}
	if user == nil || user.ID == 0 || !user.Bot {
		return "", errors.New("Discord token did not resolve to a bot user")
	}
	return user.ID.String(), nil
}
