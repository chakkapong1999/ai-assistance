package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/chakkapong1999/ai-assistance/backend/internal/webhook"
)

const defaultMaxWebhookBody = 5 << 20 // 5 MiB

// Event is one verified delivery, ready to be stored.
type Event struct {
	RequestUUID string // X-Request-UUID, the idempotency key
	EventKey    string // X-Event-Key, for example repo:push
	Payload     []byte // raw JSON body, stored as-is
	// Enqueue is true when this delivery must start processing. It is decided
	// from the event key so the store does not need to know about Bitbucket.
	Enqueue bool
}

// EventStore persists a delivery. Implementations must be atomic: the event
// row and its job are written in one transaction, and a repeated RequestUUID
// is not an error but reports inserted=false and enqueues nothing.
type EventStore interface {
	Ingest(ctx context.Context, e Event) (inserted bool, err error)
}

type webhookHandler struct {
	secret  []byte
	events  EventStore
	maxBody int64
}

func newWebhookHandler(d Deps) http.Handler {
	max := d.MaxWebhookBody
	if max <= 0 {
		max = defaultMaxWebhookBody
	}
	return &webhookHandler{secret: d.WebhookSecret, events: d.Events, maxBody: max}
}

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, errBody("payload too large"))
			return
		}
		writeJSON(w, http.StatusBadRequest, errBody("could not read body"))
		return
	}

	// Verify before looking at anything else. The answer never says why.
	if !webhook.Verify(h.secret, body, r.Header.Get("X-Hub-Signature")) {
		writeJSON(w, http.StatusUnauthorized, errBody("invalid signature"))
		return
	}

	eventKey := strings.TrimSpace(r.Header.Get("X-Event-Key"))
	requestUUID := strings.TrimSpace(r.Header.Get("X-Request-UUID"))
	if eventKey == "" || requestUUID == "" {
		writeJSON(w, http.StatusBadRequest, errBody("X-Event-Key and X-Request-UUID are required"))
		return
	}
	if !json.Valid(body) {
		writeJSON(w, http.StatusBadRequest, errBody("body is not valid JSON"))
		return
	}

	inserted, err := h.events.Ingest(r.Context(), Event{
		RequestUUID: requestUUID,
		EventKey:    eventKey,
		Payload:     body,
		Enqueue:     webhook.ShouldProcess(eventKey),
	})
	if err != nil {
		// 5xx makes Bitbucket retry the delivery with the same request UUID.
		slog.Error("webhook ingest failed", "error", err, "request_uuid", requestUUID, "event_key", eventKey)
		writeJSON(w, http.StatusInternalServerError, errBody("could not store event"))
		return
	}

	status := "accepted"
	if !inserted {
		status = "duplicate"
	}
	slog.Info("webhook received", "status", status, "request_uuid", requestUUID, "event_key", eventKey)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": status})
}

func errBody(msg string) map[string]string { return map[string]string{"error": msg} }
