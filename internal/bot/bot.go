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
	if update.CallbackQuery != nil {
		s.handleCallbackQuery(ctx, update.CallbackQuery)
		return
	}

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

	if strings.HasPrefix(body, "/") {
		s.handleCommand(ctx, message, body)
		return
	}

	if s.cfg.AdDetectionEnabled && s.isGroup(message.Chat) && !s.isAdmin(message.From.ID) {
		s.handleModeration(ctx, message, body)
	}
}

func (s *Service) handleNewMembers(ctx context.Context, message *telegram.Message) {
	s.scheduleAutoDelete(ctx, message.Chat.ID, message.MessageID, s.cfg.AutoCleanupDelay)

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
			if _, err := s.sendAutoMessage(ctx, message.Chat.ID, text, message.MessageID); err != nil {
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

	options, err := generateVerificationOptions(answer)
	if err != nil {
		s.logger.Printf("generate verification options failed: %v", err)
		return
	}

	text := fmt.Sprintf(
		"\u8bf7 %s \u5728 %s \u5185\u70b9\u51fb\u6b63\u786e\u7b54\u6848\u5b8c\u6210\u5165\u7fa4\u9a8c\u8bc1\uff1a\n%s",
		displayName(member),
		formatDurationForUser(s.cfg.VerificationTimeout),
		challenge.Question,
	)
	markup := verificationMarkup(challenge.ID, options)
	sent, err := s.client.SendMessageWithMarkupAndGet(ctx, message.Chat.ID, text, message.MessageID, &markup)
	if err != nil {
		s.logger.Printf("send verification challenge failed: %v", err)
		return
	}
	if err := s.db.SetVerificationMessageID(ctx, challenge.ID, sent.MessageID); err != nil {
		s.logger.Printf("store verification message id failed: %v", err)
	}
}

func (s *Service) handleCallbackQuery(ctx context.Context, query *telegram.CallbackQuery) {
	if query.Message == nil || !strings.HasPrefix(query.Data, "join_verify:") {
		return
	}

	challengeID, choice, err := parseVerificationCallback(query.Data)
	if err != nil {
		s.answerCallback(ctx, query.ID, "\u9a8c\u8bc1\u6309\u94ae\u65e0\u6548\u3002", true)
		return
	}

	challenge, ok, err := s.db.ActiveVerificationChallenge(ctx, query.Message.Chat.ID, query.From.ID, time.Now().UTC())
	if err != nil {
		s.logger.Printf("load verification challenge failed: %v", err)
		s.answerCallback(ctx, query.ID, "\u9a8c\u8bc1\u5904\u7406\u5931\u8d25\uff0c\u8bf7\u7a0d\u540e\u91cd\u8bd5\u3002", true)
		return
	}
	if !ok {
		s.answerCallback(ctx, query.ID, "\u9a8c\u8bc1\u5df2\u8fc7\u671f\u6216\u4e0d\u5b58\u5728\u3002", true)
		return
	}
	if challenge.ID != challengeID {
		s.answerCallback(ctx, query.ID, "\u8fd9\u4e0d\u662f\u4f60\u5f53\u524d\u7684\u9a8c\u8bc1\u9898\u3002", true)
		return
	}

	if choice != challenge.Answer {
		if err := s.db.MarkVerificationFailed(ctx, challenge.ID); err != nil {
			s.logger.Printf("mark verification failed failed: %v", err)
		}
		s.deleteVerificationMessage(ctx, query.Message.Chat.ID, query.Message.MessageID, challenge.VerificationMessageID)
		s.answerCallback(ctx, query.ID, "\u7b54\u6848\u9519\u8bef\uff0c\u5c06\u4e34\u65f6\u79fb\u51fa\u7fa4\u3002", true)
		s.temporarilyBanForVerification(ctx, challenge.ChatID, challenge.UserID, "join verification wrong answer", "temporary_ban_wrong_answer")
		return
	}

	if err := s.db.MarkVerificationPassed(ctx, challenge.ID); err != nil {
		s.logger.Printf("mark verification passed failed: %v", err)
		s.answerCallback(ctx, query.ID, "\u9a8c\u8bc1\u5904\u7406\u5931\u8d25\uff0c\u8bf7\u7a0d\u540e\u91cd\u8bd5\u3002", true)
		return
	}

	s.answerCallback(ctx, query.ID, "\u9a8c\u8bc1\u901a\u8fc7\u3002", false)
	s.deleteVerificationMessage(ctx, query.Message.Chat.ID, query.Message.MessageID, challenge.VerificationMessageID)
	name := displayName(query.From)
	passText := fmt.Sprintf("\u9a8c\u8bc1\u901a\u8fc7\uff0c\u6b22\u8fce %s\u3002", name)
	if s.cfg.WelcomeMessage != "" {
		passText = fmt.Sprintf(s.cfg.WelcomeMessage, name)
	}
	if _, err := s.sendAutoMessage(ctx, challenge.ChatID, passText, 0); err != nil {
		s.logger.Printf("send verification pass message failed: %v", err)
	}
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
		if challenge.VerificationMessageID > 0 {
			s.deleteMessage(ctx, challenge.ChatID, challenge.VerificationMessageID)
		}
		if !s.cfg.VerificationKickOnTimeout {
			continue
		}
		s.temporarilyBanForVerification(ctx, challenge.ChatID, challenge.UserID, "join verification timeout", "temporary_ban_timeout")
	}
}

func (s *Service) temporarilyBanForVerification(ctx context.Context, chatID int64, userID int64, reason string, action string) {
	if err := s.client.TemporarilyBanChatMember(ctx, chatID, userID, s.cfg.VerificationBanDuration); err != nil {
		s.logger.Printf("temporary ban verification user failed: %v", err)
		return
	}
	if err := s.db.RecordModeration(ctx, chatID, userID, 0, reason, action); err != nil {
		s.logger.Printf("record verification temporary ban failed: %v", err)
	}
}

func (s *Service) answerCallback(ctx context.Context, callbackQueryID string, text string, showAlert bool) {
	if err := s.client.AnswerCallbackQuery(ctx, callbackQueryID, text, showAlert); err != nil {
		s.logger.Printf("answer callback query failed: %v", err)
	}
}

func (s *Service) sendAutoMessage(ctx context.Context, chatID int64, text string, replyToMessageID int) (telegram.Message, error) {
	sent, err := s.client.SendMessageAndGet(ctx, chatID, text, replyToMessageID)
	if err != nil {
		return telegram.Message{}, err
	}
	s.scheduleAutoDelete(ctx, chatID, sent.MessageID, s.cfg.AutoCleanupDelay)
	return sent, nil
}

func (s *Service) scheduleAutoDelete(ctx context.Context, chatID int64, messageID int, delay time.Duration) {
	if !s.cfg.AutoCleanupEnabled || messageID <= 0 || delay <= 0 {
		return
	}
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()

		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.deleteMessage(ctx, chatID, messageID)
		}
	}()
}

func (s *Service) deleteVerificationMessage(ctx context.Context, chatID int64, callbackMessageID int, storedMessageID int) {
	messageID := storedMessageID
	if messageID == 0 {
		messageID = callbackMessageID
	}
	s.deleteMessage(ctx, chatID, messageID)
}

func (s *Service) deleteMessage(ctx context.Context, chatID int64, messageID int) {
	if messageID <= 0 {
		return
	}
	if err := s.client.DeleteMessage(ctx, chatID, messageID); err != nil {
		s.logger.Printf("delete message failed chat=%d message=%d err=%v", chatID, messageID, err)
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
			s.reply(ctx, message, "只有 Bot 创建者可以管理 RSS 订阅。")
			return
		}
		s.addRSS(ctx, message, arg)
	case "/rss_remove":
		if !s.canManage(message) {
			s.reply(ctx, message, "只有 Bot 创建者可以管理 RSS 订阅。")
			return
		}
		s.removeRSS(ctx, message, arg)
	case "/rss_list":
		if !s.canManage(message) {
			s.reply(ctx, message, "只有 Bot 创建者可以查看 RSS 订阅。")
			return
		}
		s.listRSS(ctx, message)
	case "/rss_check":
		if !s.canManage(message) {
			s.reply(ctx, message, "只有 Bot 创建者可以手动检查 RSS。")
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
	if _, err := s.sendAutoMessage(ctx, message.Chat.ID, text, message.MessageID); err != nil {
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
	return s.cfg.OwnerUserID != 0 && message.From.ID == s.cfg.OwnerUserID
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

func generateVerificationOptions(answer int) ([]int, error) {
	options := map[int]bool{answer: true}
	for len(options) < 4 {
		offset, err := randomInt(21)
		if err != nil {
			return nil, err
		}
		offset -= 10
		if offset == 0 {
			continue
		}
		candidate := answer + offset
		if candidate < 0 {
			candidate = -candidate
		}
		options[candidate] = true
	}

	result := make([]int, 0, len(options))
	for option := range options {
		result = append(result, option)
	}
	if err := shuffleInts(result); err != nil {
		return nil, err
	}
	return result, nil
}

func shuffleInts(values []int) error {
	for i := len(values) - 1; i > 0; i-- {
		j, err := randomInt(i + 1)
		if err != nil {
			return err
		}
		values[i], values[j] = values[j], values[i]
	}
	return nil
}

func verificationMarkup(challengeID int64, options []int) telegram.InlineKeyboardMarkup {
	buttons := make([][]telegram.InlineKeyboardButton, 0, 2)
	for i := 0; i < len(options); i += 2 {
		row := []telegram.InlineKeyboardButton{
			{
				Text:         strconv.Itoa(options[i]),
				CallbackData: verificationCallbackData(challengeID, options[i]),
			},
		}
		if i+1 < len(options) {
			row = append(row, telegram.InlineKeyboardButton{
				Text:         strconv.Itoa(options[i+1]),
				CallbackData: verificationCallbackData(challengeID, options[i+1]),
			})
		}
		buttons = append(buttons, row)
	}
	return telegram.InlineKeyboardMarkup{InlineKeyboard: buttons}
}

func verificationCallbackData(challengeID int64, choice int) string {
	return fmt.Sprintf("join_verify:%d:%d", challengeID, choice)
}

func parseVerificationCallback(data string) (int64, int, error) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "join_verify" {
		return 0, 0, fmt.Errorf("invalid verification callback data")
	}
	challengeID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	choice, err := strconv.Atoi(parts[2])
	if err != nil {
		return 0, 0, err
	}
	return challengeID, choice, nil
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
