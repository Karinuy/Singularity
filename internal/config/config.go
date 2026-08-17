package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	TelegramBotToken          string
	DatabasePath              string
	OwnerUserID               int64
	AdminUserIDs              map[int64]bool
	RSSPollInterval           time.Duration
	PollTimeout               int
	HTTPTimeout               time.Duration
	WelcomeMessage            string
	AutoCleanupEnabled        bool
	AutoCleanupDelay          time.Duration
	VerificationEnabled       bool
	VerificationTimeout       time.Duration
	VerificationMaxValue      int
	VerificationKickOnTimeout bool
	VerificationBanDuration   time.Duration
	AdDetectionEnabled        bool
	AdKeywords                []string
	AdScoreThreshold          int
	AdLinkLimit               int
	BanAdvertisers            bool
	RSSMaxItemsPerPoll        int
}

func Load() (Config, error) {
	cfg := Config{
		TelegramBotToken:          strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		DatabasePath:              envString("DATABASE_PATH", "data/singularity.db"),
		OwnerUserID:               envInt64("BOT_OWNER_USER_ID", 0),
		AdminUserIDs:              parseIDSet(os.Getenv("BOT_ADMIN_USER_IDS")),
		RSSPollInterval:           envDuration("BOT_RSS_POLL_INTERVAL", 5*time.Minute),
		PollTimeout:               envInt("BOT_POLL_TIMEOUT_SECONDS", 50),
		HTTPTimeout:               envDuration("BOT_HTTP_TIMEOUT", 20*time.Second),
		WelcomeMessage:            envString("BOT_WELCOME_MESSAGE", "\u6b22\u8fce %s \u5165\u7fa4\u3002"),
		AutoCleanupEnabled:        envBool("BOT_AUTO_CLEANUP_ENABLED", true),
		AutoCleanupDelay:          envDuration("BOT_AUTO_CLEANUP_DELAY", 2*time.Minute),
		VerificationEnabled:       envBool("BOT_VERIFICATION_ENABLED", true),
		VerificationTimeout:       envDuration("BOT_VERIFICATION_TIMEOUT", 3*time.Minute),
		VerificationMaxValue:      envInt("BOT_VERIFICATION_MAX_VALUE", 20),
		VerificationKickOnTimeout: envBool("BOT_VERIFICATION_KICK_ON_TIMEOUT", true),
		VerificationBanDuration:   envDuration("BOT_VERIFICATION_BAN_DURATION", 300*time.Second),
		AdDetectionEnabled:        envBool("BOT_AD_DETECTION_ENABLED", false),
		AdKeywords:                parseList(envString("BOT_AD_KEYWORDS", defaultAdKeywords())),
		AdScoreThreshold:          envInt("BOT_AD_SCORE_THRESHOLD", 3),
		AdLinkLimit:               envInt("BOT_AD_LINK_LIMIT", 2),
		BanAdvertisers:            envBool("BOT_BAN_ADVERTISERS", true),
		RSSMaxItemsPerPoll:        envInt("BOT_RSS_MAX_ITEMS_PER_POLL", 5),
	}

	if cfg.TelegramBotToken == "" {
		return Config{}, errors.New("TELEGRAM_BOT_TOKEN is required")
	}
	if cfg.RSSMaxItemsPerPoll < 1 {
		cfg.RSSMaxItemsPerPoll = 1
	}
	if cfg.AdScoreThreshold < 1 {
		cfg.AdScoreThreshold = 1
	}
	if cfg.VerificationTimeout < 30*time.Second {
		cfg.VerificationTimeout = 30 * time.Second
	}
	if cfg.VerificationMaxValue < 5 {
		cfg.VerificationMaxValue = 5
	}
	if cfg.VerificationBanDuration < 30*time.Second {
		cfg.VerificationBanDuration = 300 * time.Second
	}
	if cfg.AutoCleanupDelay < 10*time.Second {
		cfg.AutoCleanupDelay = 10 * time.Second
	}

	return cfg, nil
}

func envString(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt64(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if item != "" {
			result = append(result, strings.ToLower(item))
		}
	}
	return result
}

func parseIDSet(value string) map[int64]bool {
	result := map[int64]bool{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err == nil {
			result[id] = true
		}
	}
	return result
}

func defaultAdKeywords() string {
	return "\u52a0\u5fae\u4fe1,\u5fae\u4fe1,\u52a0v,vx,\u4ee3\u7406,\u8fd4\u4f63,\u8fd4\u5229,\u63a8\u5e7f,\u5237\u5355,\u517c\u804c,\u8d37\u6b3e,\u535a\u5f69,\u5a31\u4e50\u57ce,\u7a7a\u6295,\u7a33\u8d5a,\u79c1\u804a,t.me/,telegram.me/"
}
