// Package telegram is the admin Telegram bot: it notifies linked admins when
// uploads are processed and analysed, and accepts videos sent to it.
//
// The public Bot API (api.telegram.org) lets bots download files of at most
// 20 MB. For real practice videos run a local Bot API server
// (github.com/tdlib/telegram-bot-api with --local) and point TELEGRAM_API_URL
// at it: it downloads files of any size Telegram allows (2 GB, 4 GB with
// Premium) and returns them as paths on a shared disk.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type Client struct {
	BaseURL string // e.g. https://api.telegram.org or http://telegram-bot-api:8081
	Token   string
	HTTP    *http.Client
}

// APIError is an error answer from the Bot API.
type APIError struct {
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string { return fmt.Sprintf("telegram API %d: %s", e.Code, e.Description) }

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type Video struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
	Duration int    `json:"duration"`
}

type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type Message struct {
	MessageID int64     `json:"message_id"`
	From      *User     `json:"from"`
	Chat      Chat      `json:"chat"`
	Text      string    `json:"text"`
	Caption   string    `json:"caption"`
	Video     *Video    `json:"video"`
	Document  *Document `json:"document"`
}

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type File struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return "https://api.telegram.org"
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient // no global timeout: long polls and large downloads; callers set deadlines
}

// redact keeps the bot token (part of every URL) out of errors and logs.
func (c *Client) redact(err error) error {
	if err == nil || c.Token == "" || !strings.Contains(err.Error(), c.Token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), c.Token, "<token>"))
}

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/bot"+c.Token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return c.redact(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return c.redact(err)
	}
	defer resp.Body.Close()
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&env); err != nil {
		return fmt.Errorf("telegram %s: HTTP %s: %w", method, resp.Status, err)
	}
	if !env.OK {
		return &APIError{Code: env.ErrorCode, Description: env.Description, RetryAfter: env.Parameters.RetryAfter}
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", struct{}{}, &u)
	return u, err
}

// DeleteWebhook makes sure getUpdates works (a webhook blocks long polling).
func (c *Client) DeleteWebhook(ctx context.Context) error {
	return c.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	var ups []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": timeoutSec, "allowed_updates": []string{"message"},
	}, &ups)
	return ups, err
}

// SendMessage sends HTML-formatted text. replyTo may be 0.
func (c *Client) SendMessage(ctx context.Context, chatID int64, html string, replyTo int64) error {
	params := map[string]any{
		"chat_id": chatID, "text": html, "parse_mode": "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if replyTo != 0 {
		params["reply_parameters"] = map[string]any{"message_id": replyTo, "allow_sending_without_reply": true}
	}
	return c.call(ctx, "sendMessage", params, nil)
}

// DeleteMessage deletes a message (bots may delete incoming messages in
// private chats within 48 hours).
func (c *Client) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	return c.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}

// GetFile prepares a file for download. With a local Bot API server this is
// where the server downloads the file from Telegram, so it can take minutes.
func (c *Client) GetFile(ctx context.Context, fileID string) (File, error) {
	var f File
	err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f)
	return f, err
}

// OpenFile opens a file returned by GetFile. A local Bot API server (--local)
// returns an absolute path on its disk, which must be shared with this
// process at the same path; localPath is then set so the caller can delete
// the server's copy afterwards.
func (c *Client) OpenFile(ctx context.Context, f File) (rc io.ReadCloser, localPath string, err error) {
	if filepath.IsAbs(f.FilePath) {
		fh, err := os.Open(f.FilePath)
		if err != nil {
			return nil, "", fmt.Errorf("local Bot API file is not readable here (share its --dir volume at the same path): %w", err)
		}
		return fh, f.FilePath, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/file/bot"+c.Token+"/"+f.FilePath, nil)
	if err != nil {
		return nil, "", c.redact(err)
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, "", c.redact(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", fmt.Errorf("telegram file download: HTTP %s", resp.Status)
	}
	return resp.Body, "", nil
}
