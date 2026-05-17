package reminder

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"sinau/internal/domain"
	"sinau/internal/i18n"
)

// TelegramConfig holds credentials for the Telegram Bot API. Leaving
// BotToken empty disables the channel — the notifier silently degrades to
// its fallback (the log channel by default), matching how EmailNotifier and
// WhatsAppNotifier behave when their backend is not configured.
//
// The Bot API is plain HTTPS at https://api.telegram.org/bot<token>/<method>.
// No third-party SDK is needed.
type TelegramConfig struct {
	BotToken string // e.g. "123456:ABC-XYZ"
	APIBase  string // override only for testing; defaults to "https://api.telegram.org".
}

// TelegramNotifier is a DI-ready stub. The Notifier interface, registry
// wiring, Recipient.Telegram field, and notification_prefs.telegram_chat_id
// column are all in place. To finish wiring, replace the body of
// NotifyTaskDue with an HTTP call to sendMessage.
//
// TODO(wiring):
//   - GET/POST <APIBase>/bot<BotToken>/sendMessage?chat_id=<to.Telegram>&text=<body>
//     (urlencoded — easier than JSON for this endpoint).
//   - Parse the {"ok": bool, "description": "..."} response; treat ok=true
//     as success.
//   - Honour ctx (use http.NewRequestWithContext).
//   - Users must /start the bot once before it can DM them. Document that
//     onboarding step on /help or /settings when wiring lands.
type TelegramNotifier struct {
	cfg      TelegramConfig
	fallback Notifier
	client   *http.Client
}

func NewTelegramNotifier(cfg TelegramConfig, fallback Notifier) *TelegramNotifier {
	if fallback == nil {
		fallback = LogNotifier{}
	}
	if strings.TrimSpace(cfg.APIBase) == "" {
		cfg.APIBase = "https://api.telegram.org"
	}
	return &TelegramNotifier{
		cfg:      cfg,
		fallback: fallback,
		client:   &http.Client{Timeout: 15 * time.Second},
	}
}

// Configured reports whether real delivery is plumbed. False means the bot
// token has not been provided and any send will use the fallback.
func (t *TelegramNotifier) Configured() bool {
	return strings.TrimSpace(t.cfg.BotToken) != ""
}

func (t *TelegramNotifier) NotifyTaskDue(ctx context.Context, to Recipient, rem domain.TaskReminder) error {
	if !t.Configured() {
		log.Print("telegram notifier not configured (SINAU_TELEGRAM_BOT_TOKEN unset); routing to fallback")
		return t.fallback.NotifyTaskDue(ctx, to, rem)
	}
	if to.Telegram == "" {
		log.Printf("recipient user=%s opted for telegram but no telegram_chat_id on file; routing to fallback", to.UserID)
		return t.fallback.NotifyTaskDue(ctx, to, rem)
	}
	// Stub: prove DI works end-to-end. Replace this block with the real
	// Bot API call when wiring lands. We return nil so the worker proceeds
	// to MarkTaskReminded — no retry storm against a non-existent API.
	lang := i18n.Lang(to.Language)
	if !i18n.IsValid(lang) {
		lang = i18n.Default
	}
	body := i18n.Tf(lang, "notif.task_due.short", rem.Title, rem.RoomName, rem.DueDate)
	log.Printf("[stub] telegram would send kind=task to=%s lang=%s body=%q",
		to.Telegram, lang, body)
	_ = t.client // silence "unused" until the real call lands.
	return nil
}

func (t *TelegramNotifier) NotifyEngagement(ctx context.Context, to Recipient, ev EngagementEvent) error {
	if !t.Configured() {
		log.Print("telegram notifier not configured (SINAU_TELEGRAM_BOT_TOKEN unset); routing to fallback")
		return t.fallback.NotifyEngagement(ctx, to, ev)
	}
	if to.Telegram == "" {
		log.Printf("recipient user=%s opted for telegram but no telegram_chat_id on file; routing to fallback", to.UserID)
		return t.fallback.NotifyEngagement(ctx, to, ev)
	}
	lang := i18n.Lang(to.Language)
	if !i18n.IsValid(lang) {
		lang = i18n.Default
	}
	body := engagementSubject(lang, ev)
	log.Printf("[stub] telegram would send kind=%s to=%s lang=%s body=%q",
		ev.Kind, to.Telegram, lang, body)
	_ = t.client
	return nil
}

