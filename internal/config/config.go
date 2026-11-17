// Package config loads the small, explicit configuration shared by the bots.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Common struct {
	Token             string
	GuildID           string
	StateFile         string
	ReconcileInterval time.Duration
	RequestTimeout    time.Duration
	Workers           int
	QueueCapacity     int
}

type Owner struct {
	Common
	CategoryIDs []string
}

type Links struct {
	Common
	ChannelIDs []string
	MaxLinks   int
	Retention  time.Duration
}

// LoadEnvFile supports KEY=value, single/double quotes, and comment-only lines.
// Existing environment variables win. Values are never expanded or executed.
// Inline # characters are literal; put comments on their own lines.
func LoadEnvFile(path string) error {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open env file: %w", err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 1024), 64*1024)
	values := make(map[string]string)
	for line := 1; s.Scan(); line++ {
		text := strings.TrimSpace(strings.TrimPrefix(s.Text(), "\ufeff"))
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || !validKey(key) {
			return fmt.Errorf("invalid env assignment on line %d", line)
		}
		if _, exists := values[key]; exists {
			return fmt.Errorf("duplicate env key on line %d", line)
		}
		if strings.HasPrefix(value, "\"") {
			parsed, err := strconv.Unquote(value)
			if err != nil {
				return fmt.Errorf("invalid double-quoted env value on line %d", line)
			}
			value = parsed
		} else if strings.HasPrefix(value, "'") {
			if len(value) < 2 || !strings.HasSuffix(value, "'") || strings.Contains(value[1:len(value)-1], "'") {
				return fmt.Errorf("invalid single-quoted env value on line %d", line)
			}
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	if err := s.Err(); err != nil {
		return fmt.Errorf("read env file: %w", err)
	}
	for key, value := range values {
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("set env variable %s: %w", key, err)
			}
		}
	}
	return nil
}

func validKey(key string) bool {
	if key == "" {
		return false
	}
	for i, ch := range key {
		if ch != '_' && !(ch >= 'A' && ch <= 'Z') && !(ch >= 'a' && ch <= 'z') && !(i > 0 && ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

func ReadOwner() (Owner, error) {
	c, err := common("./data/owner.json", 60)
	if err != nil {
		return Owner{}, err
	}
	ids, err := readIDs("CATEGORY_IDS", true)
	return Owner{Common: c, CategoryIDs: ids}, err
}

func ReadLinks() (Links, error) {
	c, err := common("./data/links.json", 300)
	if err != nil {
		return Links{}, err
	}
	ids, err := readIDs("CHANNEL_IDS", false)
	if err != nil {
		return Links{}, err
	}
	maxLinks, err := integer("MAX_LINKS", 5, 1, 5)
	if err != nil {
		return Links{}, err
	}
	days, err := integer("RETENTION_DAYS", 7, 1, 90)
	if err != nil {
		return Links{}, err
	}
	return Links{Common: c, ChannelIDs: ids, MaxLinks: maxLinks, Retention: time.Duration(days) * 24 * time.Hour}, nil
}

func common(defaultState string, defaultInterval int) (Common, error) {
	c := Common{Token: strings.TrimSpace(os.Getenv("DISCORD_TOKEN")), GuildID: strings.TrimSpace(os.Getenv("GUILD_ID"))}
	if c.Token == "" || strings.Contains(strings.ToLower(c.Token), "replace_") || strings.ContainsAny(c.Token, "\r\n\t ") {
		return c, errors.New("set DISCORD_TOKEN to the bot token")
	}
	if !ValidID(c.GuildID) {
		return c, errors.New("GUILD_ID must be a nonzero Discord snowflake")
	}
	c.StateFile = strings.TrimSpace(os.Getenv("STATE_FILE"))
	if c.StateFile == "" {
		c.StateFile = defaultState
	}
	interval, err := integer("RECONCILE_INTERVAL_SECONDS", defaultInterval, 15, 86400)
	if err != nil {
		return c, err
	}
	c.ReconcileInterval = time.Duration(interval) * time.Second
	timeout, err := integer("REQUEST_TIMEOUT_SECONDS", 30, 5, 300)
	if err != nil {
		return c, err
	}
	c.RequestTimeout = time.Duration(timeout) * time.Second
	c.Workers, err = integer("WORKERS", 4, 1, 16)
	if err != nil {
		return c, err
	}
	c.QueueCapacity, err = integer("QUEUE_CAPACITY", 1024, 16, 10000)
	return c, err
}

func readIDs(key string, required bool) ([]string, error) {
	seen := make(map[string]bool)
	var ids []string
	for _, value := range strings.Split(os.Getenv(key), ",") {
		id := strings.TrimSpace(value)
		if id == "" {
			continue
		}
		if !ValidID(id) {
			return nil, fmt.Errorf("%s must contain comma-separated Discord snowflakes", key)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if required && len(ids) == 0 {
		return nil, fmt.Errorf("set %s", key)
	}
	return ids, nil
}

func ValidID(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	return err == nil && n != 0
}

func integer(key string, fallback, min, max int) (int, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", key, min, max)
	}
	return n, nil
}
