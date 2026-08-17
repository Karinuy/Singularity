package bot

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"time"

	"singularity/internal/config"
	"singularity/internal/moderation"
	"singularity/internal/rss"
	"singularity/internal/storage"
	"singularity/internal/telegram"
)

type Service struct {
	cfg    config.Config
	db     *storage.DB
	client *telegram.Client
	logger *log.Logger
}

func New(cfg config.Config, db *storage.DB, client *telegram.Client, logger *log.Logger) *Service {
	return &Service{
		cfg:    cfg,
		db:     db,
		client: client,
		logger: logger,
	}
}

func (s *Service) Run(ctx context.Context) error {
	s.logger.Printf("starting bot")

	go s.runRSSPoller(ctx)
	if s.cfg.VerificationEnabled {
		go s.runVerificationSweeper(ctx)
	}

	offset := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		updates, err := s.client.GetUpdates(ctx, offset, s.cfg.PollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.logger.Printf("get updates failed: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}

		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			s.handleUpdate(ctx, update)
		}
	}
}

func (s *Service) handleUpdate(ctx context.Context, update telegram.Update) {
	message := update.Message
	if message == nil {
		message = update.EditedMessage
	}
	if message == nil {
		return
	}

	if len(message.NewChatMembers) > 0 {
		s.handleNewMembers(ctx, message)
	}

	body := strings.TrimSpace(firstNonEmpty(message.Text, message.Caption))
	if body == "" || message.From == nil {
		return
	}

	if s.cfg.VerificationEnabled && s.isGroup(message.Chat) && !s.isAdmin(message.From.ID) {
		handled, err := s.handleVerificationResponse(ctx, message, body)
		if err != nil {
			s.logger.Printf("handle verification response failed: %v", err)
		}
		if handled {
			return
		}
	}

	if strings.HasPrefix(body, "/") {
		s.handleCommand(ctx, message, body)
		return
	}

	if s.cfg.AdDetectionEnabled && s.isGroup(message.Chat) && !s.isAdmin(message.From.ID) {
		s.handleModeration(ctx, message, body)
	}
}

func (s *Service) handleNewMembers(ctx context.Context, message *telegram.Message) {
	for _, member := range message.NewChatMembers {
		event := storage.JoinEvent{
			ChatID:    message.Chat.ID,
			UserID:    member.ID,
			Username:  member.Username,
			FirstName: member.FirstName,
			JoinedAt:  time.Now().UTC(),
		}
		if err := s.db.RecordJoin(ctx, event); err != nil {
			s.logger.Printf("record join failed: %v", err)
		}

		if member.IsBot {
			continue
		}

		if s.cfg.VerificationEnabled && s.isGroup(message.Chat) {
			s.startVerification(ctx, message, member)
			continue
		}

		name := displayName(member)
		if s.cfg.WelcomeMessage != "" {
			text := fmt.Sprintf(s.cfg.WelcomeMessage, name)
			if err := s.client.SendMessage(ctx, message.Chat.ID, text, message.MessageID); err != nil {
				s.logger.Printf("send welcome failed: %v", err)
			}
		}
	}
}

func (s *Service) startVerification(ctx context.Context, message *telegram.Message, member telegram.User) {
	question, answer, err := generateArithmeticChallenge(s.cfg.VerificationMaxValue)
	if err != nil {
		s.logger.Printf("generate verification challenge failed: %v", err)
		return
	}

	expiresAt := time.Now().UTC().Add(s.cfg.VerificationTimeout)
	challenge, err := s.db.CreateVerificationChallenge(ctx, message.Chat.ID, member.ID, question, answer, expiresAt)
	if err != nil {
		s.logger.Printf("create verification challenge failed: %v", err)
		return
	}

	text := fmt.Sprintf(
		"\u8bf7 %s \u5728 %s \u5185\u56de\u590d\u7b97\u672f\u9898\u7b54\u6848\u5b8c\u6210\u5165\u7fa4\u9a8c\u8bc1\uff1a\n%s",
		displayName(member),
		formatDurationForUser(s.cfg.VerificationTimeout),
		challenge.Question,
	)
	if err := s.client.SendMessage(ctx, message.Chat.ID, text, message.MessageID); err != nil {
		s.logger.Printf("send verification challenge failed: %v", err)
	}
}

func (s *Service) handleVerificationResponse(ctx context.Context, message *telegram.Message, body string) (bool, error) {
	challenge, ok, err := s.db.ActiveVerificationChallenge(ctx, message.Chat.ID, message.From.ID, time.Now().UTC())
	if err != nil || !ok {
		return false, err
	}

	answer, err := strconv.Atoi(strings.TrimSpace(body))
	if err != nil || answer != challenge.Answer {
		s.reply(ctx, message, "\u7b54\u6848\u4e0d\u6b63\u786e\uff0c\u8bf7\u76f4\u63a5\u56de\u590d\u7b97\u672f\u9898\u7684\u6570\u5b57\u7b54\u6848\u3002")
		return true, nil
	}

	if err := s.db.MarkVerificationPassed(ctx, challenge.ID); err != nil {
		return true, err
	}

	name := displayName(*message.From)
	passText := fmt.Sprintf("\u9a8c\u8bc1\u901a\u8fc7\uff0c\u6b22\u8fce %s\u3002", name)
	if s.cfg.WelcomeMessage != "" {
		passText = fmt.Sprintf(s.cfg.WelcomeMessage, name)
	}
	s.reply(ctx, message, passText)
	return true, nil
}

func (s *Service) runVerificationSweeper(ctx context.Context) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.expirePendingVerifications(ctx)
			timer.Reset(30 * time.Second)
		}
	}
}

func (s *Service) expirePendingVerifications(ctx context.Context) {
	challenges, err := s.db.ExpiredVerificationChallenges(ctx, time.Now().UTC(), 50)
	if err != nil {
		s.logger.Printf("load expired verification challenges failed: %v", err)
		return
	}

	for _, challenge := range challenges {
		if err := s.db.MarkVerificationExpired(ctx, challenge.ID); err != nil {
			s.logger.Printf("mark verification expired failed: %v", err)
			continue
		}
		if !s.cfg.VerificationKickOnTimeout {
			continue
		}
		if err := s.client.KickChatMember(ctx, challenge.ChatID, challenge.UserID); err != nil {
			s.logger.Printf("kick verification timeout user failed: %v", err)
			continue
		}
		if err := s.db.RecordModeration(ctx, challenge.ChatID, challenge.UserID, 0, "join verification timeout", "kick"); err != nil {
			s.logger.Printf("record verification timeout failed: %v", err)
		}
	}
}

func (s *Service) handleModeration(ctx context.Context, message *telegram.Message, body string) {
	result := moderation.DetectAdvertisement(body, s.cfg.AdKeywords, s.cfg.AdScoreThreshold, s.cfg.AdLinkLimit)
	if !result.Advertisement {
		return
	}

	action := "delete"
	if err := s.client.DeleteMessage(ctx, message.Chat.ID, message.MessageID); err != nil {
		s.logger.Printf("delete ad message failed: %v", err)
	}

	if s.cfg.BanAdvertisers {
		action = "delete_and_ban"
		if err := s.client.BanChatMember(ctx, message.Chat.ID, message.From.ID); err != nil {
			s.logger.Printf("ban advertiser failed: %v", err)
		}
	}

	if err := s.db.RecordModeration(ctx, message.Chat.ID, message.From.ID, message.MessageID, result.Reason, action); err != nil {
		s.logger.Printf("record moderation failed: %v", err)
	}
	s.logger.Printf("moderated user=%d chat=%d action=%s reason=%s score=%d", message.From.ID, message.Chat.ID, action, result.Reason, result.Score)
}

func (s *Service) handleCommand(ctx context.Context, message *telegram.Message, body string) {
	command, arg := splitCommand(body)
	switch command {
	case "/start", "/help":
		s.reply(ctx, message, helpText())
	case "/rss_add":
		if !s.canManage(message) {
			s.reply(ctx, message, "没有权限管理 RSS 订阅。")
			return
		}
		s.addRSS(ctx, message, arg)
	case "/rss_remove":
		if !s.canManage(message) {
			s.reply(ctx, message, "没有权限管理 RSS 订阅。")
			return
		}
		s.removeRSS(ctx, message, arg)
	case "/rss_list":
		s.listRSS(ctx, message)
	case "/rss_check":
		if !s.canManage(message) {
			s.reply(ctx, message, "没有权限手动检查 RSS。")
			return
		}
		s.pollRSS(ctx)
		s.reply(ctx, message, "RSS 检查已完成。")
	}
}

func (s *Service) addRSS(ctx context.Context, message *telegram.Message, rawURL string) {
	feedURL, err := normalizeFeedURL(rawURL)
	if err != nil {
		s.reply(ctx, message, "用法：/rss_add <http-or-https-url>")
		return
	}

	feed, err := rss.Fetch(ctx, feedURL, s.cfg.HTTPTimeout)
	if err != nil {
		s.reply(ctx, message, "RSS 拉取失败："+err.Error())
		return
	}

	sub, err := s.db.AddSubscription(ctx, message.Chat.ID, feedURL, feed.Title, message.From.ID)
	if err != nil {
		s.reply(ctx, message, "RSS 订阅保存失败。")
		s.logger.Printf("add subscription failed: %v", err)
		return
	}

	for _, item := range feed.Items {
		if err := s.db.MarkItemSent(ctx, sub.ID, item.Key, item.Link, item.PublishedAt); err != nil {
			s.logger.Printf("mark initial rss item failed: %v", err)
		}
	}

	title := firstNonEmpty(feed.Title, feedURL)
	s.reply(ctx, message, "已订阅："+title)
}

func (s *Service) removeRSS(ctx context.Context, message *telegram.Message, rawURL string) {
	feedURL, err := normalizeFeedURL(rawURL)
	if err != nil {
		s.reply(ctx, message, "用法：/rss_remove <http-or-https-url>")
		return
	}

	removed, err := s.db.RemoveSubscription(ctx, message.Chat.ID, feedURL)
	if err != nil {
		s.reply(ctx, message, "RSS 订阅删除失败。")
		s.logger.Printf("remove subscription failed: %v", err)
		return
	}
	if !removed {
		s.reply(ctx, message, "没有找到这个 RSS 订阅。")
		return
	}
	s.reply(ctx, message, "已移除订阅。")
}

func (s *Service) listRSS(ctx context.Context, message *telegram.Message) {
	subs, err := s.db.ListSubscriptions(ctx, message.Chat.ID)
	if err != nil {
		s.reply(ctx, message, "RSS 订阅读取失败。")
		s.logger.Printf("list subscriptions failed: %v", err)
		return
	}
	if len(subs) == 0 {
		s.reply(ctx, message, "当前聊天还没有 RSS 订阅。")
		return
	}

	lines := []string{"当前 RSS 订阅："}
	for i, sub := range subs {
		title := firstNonEmpty(sub.Title, sub.FeedURL)
		lines = append(lines, fmt.Sprintf("%d. %s\n%s", i+1, title, sub.FeedURL))
	}
	s.reply(ctx, message, strings.Join(lines, "\n"))
}

func (s *Service) runRSSPoller(ctx context.Context) {
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.pollRSS(ctx)
			timer.Reset(s.cfg.RSSPollInterval)
		}
	}
}

func (s *Service) pollRSS(ctx context.Context) {
	subs, err := s.db.AllSubscriptions(ctx)
	if err != nil {
		s.logger.Printf("load rss subscriptions failed: %v", err)
		return
	}

	for _, sub := range subs {
		feed, err := rss.Fetch(ctx, sub.FeedURL, s.cfg.HTTPTimeout)
		if err != nil {
			s.logger.Printf("fetch rss failed sub=%d url=%s err=%v", sub.ID, sub.FeedURL, err)
			continue
		}
		if feed.Title != "" && feed.Title != sub.Title {
			if err := s.db.UpdateSubscriptionTitle(ctx, sub.ID, feed.Title); err != nil {
				s.logger.Printf("update feed title failed: %v", err)
			}
		}

		sent := 0
		for _, item := range feed.Items {
			if sent >= s.cfg.RSSMaxItemsPerPoll {
				break
			}
			seen, err := s.db.ItemSeen(ctx, sub.ID, item.Key)
			if err != nil {
				s.logger.Printf("check rss item failed: %v", err)
				continue
			}
			if seen {
				continue
			}

			text := formatRSSMessage(firstNonEmpty(feed.Title, sub.Title), item)
			if err := s.client.SendMessage(ctx, sub.ChatID, text, 0); err != nil {
				s.logger.Printf("send rss item failed: %v", err)
				continue
			}
			if err := s.db.MarkItemSent(ctx, sub.ID, item.Key, item.Link, item.PublishedAt); err != nil {
				s.logger.Printf("mark rss item failed: %v", err)
			}
			sent++
		}
	}
}

func (s *Service) reply(ctx context.Context, message *telegram.Message, text string) {
	if err := s.client.SendMessage(ctx, message.Chat.ID, text, message.MessageID); err != nil {
		s.logger.Printf("send reply failed: %v", err)
	}
}

func (s *Service) isGroup(chat telegram.Chat) bool {
	return chat.Type == "group" || chat.Type == "supergroup"
}

func (s *Service) canManage(message *telegram.Message) bool {
	if message.From == nil {
		return false
	}
	if len(s.cfg.AdminUserIDs) == 0 {
		return true
	}
	return s.cfg.AdminUserIDs[message.From.ID]
}

func (s *Service) isAdmin(userID int64) bool {
	return s.cfg.AdminUserIDs[userID]
}

func splitCommand(body string) (string, string) {
	fields := strings.Fields(body)
	if len(fields) == 0 {
		return "", ""
	}
	command := fields[0]
	if at := strings.Index(command, "@"); at >= 0 {
		command = command[:at]
	}
	if len(fields) == 1 {
		return command, ""
	}
	return command, strings.TrimSpace(strings.TrimPrefix(body, fields[0]))
}

func normalizeFeedURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid url")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme")
	}
	return parsed.String(), nil
}

func formatRSSMessage(feedTitle string, item rss.Item) string {
	lines := []string{"RSS 更新：" + firstNonEmpty(feedTitle, "未命名订阅")}
	if item.Title != "" {
		lines = append(lines, item.Title)
	}
	if item.Link != "" {
		lines = append(lines, item.Link)
	}
	return strings.Join(lines, "\n")
}

func displayName(user telegram.User) string {
	if user.Username != "" {
		return "@" + user.Username
	}
	name := strings.TrimSpace(strings.Join([]string{user.FirstName, user.LastName}, " "))
	if name == "" {
		return fmt.Sprintf("%d", user.ID)
	}
	return name
}

func generateArithmeticChallenge(maxValue int) (string, int, error) {
	left, err := randomInt(maxValue)
	if err != nil {
		return "", 0, err
	}
	right, err := randomInt(maxValue)
	if err != nil {
		return "", 0, err
	}
	operator, err := randomInt(3)
	if err != nil {
		return "", 0, err
	}

	left++
	right++
	switch operator {
	case 0:
		return fmt.Sprintf("%d + %d = ?", left, right), left + right, nil
	case 1:
		if left < right {
			left, right = right, left
		}
		return fmt.Sprintf("%d - %d = ?", left, right), left - right, nil
	default:
		left = left%9 + 1
		right = right%9 + 1
		return fmt.Sprintf("%d * %d = ?", left, right), left * right, nil
	}
}

func randomInt(maxValue int) (int, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(int64(maxValue)))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}

func formatDurationForUser(duration time.Duration) string {
	if duration >= time.Minute {
		minutes := int(duration.Round(time.Minute) / time.Minute)
		return fmt.Sprintf("%d \u5206\u949f", minutes)
	}
	seconds := int(duration.Round(time.Second) / time.Second)
	return fmt.Sprintf("%d \u79d2", seconds)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func helpText() string {
	return strings.Join([]string{
		"Singularity Bot",
		"/rss_add <url> - 添加 RSS 订阅",
		"/rss_remove <url> - 移除 RSS 订阅",
		"/rss_list - 查看当前聊天订阅",
		"/rss_check - 立即检查 RSS",
	}, "\n")
}
