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

// WhatsAppConfig points at a running instance of
// https://github.com/aldinokemal/go-whatsapp-web-multidevice (or any other
// WhatsApp HTTP gateway compatible with that shape). Leaving APIURL empty
// disables the channel — the notifier silently degrades to its fallback,
// the same pattern EmailNotifier uses when SMTP is not configured.
//
// The gateway runs as a separate process and exposes endpoints such as
// `POST /send/message` with `{ "phone": "+62…", "message": "…" }`. We
// intentionally do not import a WhatsApp library here so this package stays
// network-pure and easy to swap.
type WhatsAppConfig struct {
	APIURL string // e.g. "http://127.0.0.1:3000"
	APIKey string // bearer token / API key the gateway expects, if any.
}

// WhatsAppNotifier is a DI-ready stub. The Notifier interface, registry
// wiring, Recipient.WhatsApp field, and notification_prefs.whatsapp_number
// column are all in place. To finish wiring, replace the body of
// NotifyTaskDue with an HTTP call to the gateway.
//
// TODO(wiring):
//   - POST <APIURL>/send/message with JSON {"phone": to.WhatsApp, "message": body}
//   - Authorization header from cfg.APIKey (gateway-specific: bearer or basic)
//   - Treat 2xx as success; 4xx as permanent (return nil so we don't retry forever);
//     5xx / network as transient (return error so the next tick retries).
//   - Honour ctx (use http.NewRequestWithContext).
//   - Match gateway's phone-number format (E.164 with no spaces).
type WhatsAppNotifier struct {
	cfg      WhatsAppConfig
	fallback Notifier
	client   *http.Client
}

func NewWhatsAppNotifier(cfg WhatsAppConfig, fallback Notifier) *WhatsAppNotifier {
	if fallback == nil {
		fallback = LogNotifier{}
	}
	return &WhatsAppNotifier{
		cfg:      cfg,
		fallback: fallback,
		client:   &http.Client{Timeout: 15 * time.Second},
	}
}

// Configured reports whether real delivery is plumbed. False means the
// gateway URL has not been provided and any send will use the fallback.
func (w *WhatsAppNotifier) Configured() bool {
	return strings.TrimSpace(w.cfg.APIURL) != ""
}

func (w *WhatsAppNotifier) NotifyTaskDue(ctx context.Context, to Recipient, rem domain.TaskReminder) error {
	if !w.Configured() {
		log.Print("whatsapp notifier not configured (SINAU_WHATSAPP_API_URL unset); routing to fallback")
		return w.fallback.NotifyTaskDue(ctx, to, rem)
	}
	if to.WhatsApp == "" {
		log.Printf("recipient user=%s opted for whatsapp but no whatsapp_number on file; routing to fallback", to.UserID)
		return w.fallback.NotifyTaskDue(ctx, to, rem)
	}
	// Stub: prove DI is end-to-end. Replace this block with the real HTTP
	// call when the gateway is wired. We return nil so the worker proceeds
	// to MarkTaskReminded — i.e. no retry storm against a non-existent API.
	lang := i18n.Lang(to.Language)
	if !i18n.IsValid(lang) {
		lang = i18n.Default
	}
	body := i18n.Tf(lang, "notif.task_due.short", rem.Title, rem.RoomName, rem.DueDate)
	log.Printf("[stub] whatsapp would send kind=task to=%s lang=%s body=%q (apiurl=%s)",
		to.WhatsApp, lang, body, w.cfg.APIURL)
	_ = w.client // silence "unused" until the real call lands.
	return nil
}

func (w *WhatsAppNotifier) NotifyEngagement(ctx context.Context, to Recipient, ev EngagementEvent) error {
	if !w.Configured() {
		log.Print("whatsapp notifier not configured (SINAU_WHATSAPP_API_URL unset); routing to fallback")
		return w.fallback.NotifyEngagement(ctx, to, ev)
	}
	if to.WhatsApp == "" {
		log.Printf("recipient user=%s opted for whatsapp but no whatsapp_number on file; routing to fallback", to.UserID)
		return w.fallback.NotifyEngagement(ctx, to, ev)
	}
	lang := i18n.Lang(to.Language)
	if !i18n.IsValid(lang) {
		lang = i18n.Default
	}
	body := engagementSubject(lang, ev)
	log.Printf("[stub] whatsapp would send kind=%s to=%s lang=%s body=%q (apiurl=%s)",
		ev.Kind, to.WhatsApp, lang, body, w.cfg.APIURL)
	_ = w.client
	return nil
}

