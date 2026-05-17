package reminder

import (
	"context"
	"log"
	"strings"

	"sinau/internal/domain"
	"sinau/internal/store"
)

// Engagement is the synchronous-from-the-write-path counterpart to
// Worker: instead of scanning for deadlines on a tick, web handlers
// call Dispatch directly after a comment/submission/review write
// commits successfully. It pulls the recipient's notification
// preferences and language, then routes the event through the same
// notifier registry the reminder worker uses.
//
// Engagement events are best-effort: failures are logged but never
// surface to the user (the underlying write already succeeded). To
// avoid blocking HTTP requests behind SMTP/HTTP fanout, callers
// typically invoke Dispatch from a goroutine.
type Engagement struct {
	store     *store.Store
	notifiers map[string]Notifier
}

// EngagementKind enumerates the engagement events shipped at v1. The
// kind is included on the EngagementEvent so notifiers can pick a
// matching translation key without a type switch per delivery channel.
type EngagementKind string

const (
	EngagementReportComment   EngagementKind = "report_comment"
	EngagementSubmissionMade  EngagementKind = "submission_made"
	EngagementFeedbackPosted  EngagementKind = "feedback_posted"
)

// EngagementEvent is the payload Engagement.Dispatch routes through
// the notifier registry. It carries everything a notifier needs to
// render localised content without re-querying the store: the actor
// who triggered the event, the room context, an optional title (for
// assignment-bound events), a short snippet of the body, and a
// deep-link path back into Sinau.
type EngagementEvent struct {
	Kind         EngagementKind
	RecipientID  string
	ActorName    string
	RoomID       string
	RoomName     string
	RoomMode     string
	Title        string
	Snippet      string
	Score        string
	DeepLinkPath string
}

func NewEngagement(s *store.Store, notifiers map[string]Notifier) *Engagement {
	if notifiers == nil {
		notifiers = map[string]Notifier{}
	}
	if _, ok := notifiers[domain.NotifChannelLog]; !ok {
		notifiers[domain.NotifChannelLog] = LogNotifier{}
	}
	return &Engagement{store: s, notifiers: notifiers}
}

// Dispatch sends event to the recipient's chosen channel, falling back
// silently when prefs are off or the recipient has opted out of
// engagement notifications. Safe to call in a goroutine — never
// returns and never panics; transport errors are logged.
func (e *Engagement) Dispatch(ctx context.Context, ev EngagementEvent) {
	if e == nil || ev.RecipientID == "" {
		return
	}
	user, err := e.store.UserByID(ev.RecipientID)
	if err != nil {
		log.Printf("engagement dispatch lookup user=%s: %v", ev.RecipientID, err)
		return
	}
	if !user.EngagementEnabled {
		return
	}
	prefs := e.store.NotificationPrefsFor(user.ID)
	if !prefs.Enabled || prefs.Channel == domain.NotifChannelOff {
		return
	}
	notifier, ok := e.notifiers[prefs.Channel]
	if !ok {
		log.Printf("no notifier for channel=%q (engagement) user=%s", prefs.Channel, user.ID)
		return
	}
	to := Recipient{
		UserID:   user.ID,
		Name:     user.Name,
		Channel:  prefs.Channel,
		Language: user.Language,
		Email:    user.Email,
		WhatsApp: prefs.WhatsAppNumber,
		Telegram: prefs.TelegramChatID,
	}
	if err := notifier.NotifyEngagement(ctx, to, ev); err != nil {
		log.Printf("engagement send failed channel=%s kind=%s user=%s: %v",
			prefs.Channel, ev.Kind, user.ID, err)
	}
}

// Snippet trims s to roughly n runes for use in notification bodies.
// Used by web handlers to avoid emailing the full body of a 2000-char
// comment.
func Snippet(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	cut := s[:n]
	if i := strings.LastIndex(cut, " "); i > 20 {
		cut = cut[:i]
	}
	return cut + "…"
}
