package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(token string, timeout time.Duration) *Client {
	return &Client{
		baseURL: "https://api.telegram.org/bot" + token + "/",
		http: &http.Client{
			Timeout: timeout + 10*time.Second,
		},
	}
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func (c *Client) GetUpdates(ctx context.Context, offset int, timeoutSeconds int) ([]Update, error) {
	values := url.Values{}
	values.Set("offset", strconv.Itoa(offset))
	values.Set("timeout", strconv.Itoa(timeoutSeconds))
	values.Set("allowed_updates", `["message","edited_message","callback_query"]`)

	var updates []Update
	if err := c.post(ctx, "getUpdates", values, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (c *Client) SetMyCommands(ctx context.Context, commands []BotCommand) error {
	return c.SetMyCommandsForScope(ctx, commands, nil)
}

func (c *Client) SetMyCommandsForScope(ctx context.Context, commands []BotCommand, scope *BotCommandScope) error {
	encoded, err := json.Marshal(commands)
	if err != nil {
		return err
	}

	values := url.Values{}
	values.Set("commands", string(encoded))
	if scope != nil {
		encodedScope, err := json.Marshal(scope)
		if err != nil {
			return err
		}
		values.Set("scope", string(encodedScope))
	}
	return c.post(ctx, "setMyCommands", values, nil)
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, replyToMessageID int) error {
	_, err := c.SendMessageAndGet(ctx, chatID, text, replyToMessageID)
	return err
}

func (c *Client) SendMessageAndGet(ctx context.Context, chatID int64, text string, replyToMessageID int) (Message, error) {
	return c.SendMessageWithMarkupAndGet(ctx, chatID, text, replyToMessageID, nil)
}

func (c *Client) SendMessageWithMarkup(ctx context.Context, chatID int64, text string, replyToMessageID int, markup *InlineKeyboardMarkup) error {
	_, err := c.SendMessageWithMarkupAndGet(ctx, chatID, text, replyToMessageID, markup)
	return err
}

func (c *Client) SendMessageWithMarkupAndGet(ctx context.Context, chatID int64, text string, replyToMessageID int, markup *InlineKeyboardMarkup) (Message, error) {
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("text", text)
	values.Set("disable_web_page_preview", "false")
	if replyToMessageID > 0 {
		values.Set("reply_to_message_id", strconv.Itoa(replyToMessageID))
		values.Set("allow_sending_without_reply", "true")
	}
	if markup != nil {
		encoded, err := json.Marshal(markup)
		if err != nil {
			return Message{}, err
		}
		values.Set("reply_markup", string(encoded))
	}
	var sent Message
	if err := c.post(ctx, "sendMessage", values, &sent); err != nil {
		return Message{}, err
	}
	return sent, nil
}

func (c *Client) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("message_id", strconv.Itoa(messageID))
	return c.post(ctx, "deleteMessage", values, nil)
}

func (c *Client) BanChatMember(ctx context.Context, chatID int64, userID int64) error {
	return c.BanChatMemberUntil(ctx, chatID, userID, 0, true)
}

func (c *Client) BanChatMemberUntil(ctx context.Context, chatID int64, userID int64, untilDate int64, revokeMessages bool) error {
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("user_id", strconv.FormatInt(userID, 10))
	values.Set("revoke_messages", strconv.FormatBool(revokeMessages))
	if untilDate > 0 {
		values.Set("until_date", strconv.FormatInt(untilDate, 10))
	}
	return c.post(ctx, "banChatMember", values, nil)
}

func (c *Client) UnbanChatMember(ctx context.Context, chatID int64, userID int64) error {
	values := url.Values{}
	values.Set("chat_id", strconv.FormatInt(chatID, 10))
	values.Set("user_id", strconv.FormatInt(userID, 10))
	values.Set("only_if_banned", "true")
	return c.post(ctx, "unbanChatMember", values, nil)
}

func (c *Client) KickChatMember(ctx context.Context, chatID int64, userID int64) error {
	untilDate := time.Now().Add(60 * time.Second).Unix()
	if err := c.BanChatMemberUntil(ctx, chatID, userID, untilDate, true); err != nil {
		return err
	}
	return c.UnbanChatMember(ctx, chatID, userID)
}

func (c *Client) TemporarilyBanChatMember(ctx context.Context, chatID int64, userID int64, duration time.Duration) error {
	untilDate := time.Now().Add(duration).Unix()
	return c.BanChatMemberUntil(ctx, chatID, userID, untilDate, true)
}

func (c *Client) AnswerCallbackQuery(ctx context.Context, callbackQueryID string, text string, showAlert bool) error {
	values := url.Values{}
	values.Set("callback_query_id", callbackQueryID)
	if text != "" {
		values.Set("text", text)
	}
	values.Set("show_alert", strconv.FormatBool(showAlert))
	return c.post(ctx, "answerCallbackQuery", values, nil)
}

func (c *Client) post(ctx context.Context, method string, values url.Values, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+method, bytes.NewBufferString(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var decoded apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return err
	}
	if !decoded.OK {
		if decoded.Description == "" {
			decoded.Description = resp.Status
		}
		return fmt.Errorf("telegram %s failed: %s", method, decoded.Description)
	}
	if target == nil {
		return nil
	}
	return json.Unmarshal(decoded.Result, target)
}
