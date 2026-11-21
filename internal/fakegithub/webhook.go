package fakegithub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// delivery is the outcome of a webhook delivery, as a control call reports
// it: badil's status and the duration, or the error when badil was
// unreachable.
type delivery struct {
	Event      string `json:"event"`
	Action     string `json:"action,omitempty"`
	Status     int    `json:"status,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// send delivers payload as a webhook of event (and action, for logs and the
// report) to the configured webhook URL, signed with the webhook secret, and
// returns badil's answer. It does not retry.
func (s *Server) send(ctx context.Context, event, action string, payload any) delivery {
	d := delivery{Event: event, Action: action}
	start := time.Now()
	status, err := s.post(ctx, event, payload)
	d.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		d.Error = err.Error()
		slog.WarnContext(ctx, "webhook failed",
			slog.String("event", event), slog.String("action", action), slog.String("err", d.Error))
		return d
	}
	d.Status = status
	slog.InfoContext(ctx, "webhook sent",
		slog.String("event", event), slog.String("action", action),
		slog.Int("status", status), slog.Int64("duration_ms", d.DurationMS))
	return d
}

// post sends one delivery and returns its HTTP status.
func (s *Server) post(ctx context.Context, event string, payload any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("encoding the payload: %w", err)
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.WebhookSecret))
	mac.Write(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Github-Event", event)
	req.Header.Set("X-Github-Delivery", uuid.NewString())
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	resp, err := s.webhook.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
