package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Lal-Jr/Graphly/server/forecast"
	"github.com/jackc/pgx/v5"
)

// Email and Slack never go out inside a request. notify() queues a row per channel in
// notification_deliveries; this worker waits DigestWindow after a person's oldest pending row (so a
// burst becomes one digest), claims their due rows with SKIP LOCKED, sends one message, and either
// marks them sent or schedules a retry with exponential backoff. Delivery is at-least-once: a crash
// between sending and committing resends that digest.

const (
	maxDigestItems = 100
	sendTimeout    = 15 * time.Second
)

// mailer sends one plain-text email.
type mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

func (s *Server) enqueueDeliveries(ctx context.Context, tx pgx.Tx, notificationID int64, userID string, p channelPrefs) error {
	if p.Email && s.mailer != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO notification_deliveries (notification_id, user_id, channel)
			SELECT $1, u.id, 'email' FROM users u WHERE u.id = $2 AND NOT u.is_demo ON CONFLICT DO NOTHING`, notificationID, userID); err != nil {
			return err
		}
	}
	if p.Slack {
		if _, err := tx.Exec(ctx, `INSERT INTO notification_deliveries (notification_id, user_id, channel)
			SELECT $1, $2, 'slack' FROM notification_settings WHERE user_id = $2 AND slack_webhook_url IS NOT NULL ON CONFLICT DO NOTHING`,
			notificationID, userID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) deliveryLoop(ctx context.Context) {
	t := time.NewTicker(s.cfg.DeliveryPollInterval)
	defer t.Stop()
	for {
		if _, err := s.runDeliveries(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("deliveries", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runDeliveries sends every digest that's due and returns how many messages went out.
func (s *Server) runDeliveries(ctx context.Context) (int, error) {
	type group struct{ userID, channel string }
	groups, err := collect(func(row pgx.Row) (group, error) {
		var g group
		return g, row.Scan(&g.userID, &g.channel)
	})(s.db.Query(ctx, `
		SELECT user_id, channel FROM notification_deliveries
		WHERE status = 'pending' AND next_attempt_at <= now()
		GROUP BY user_id, channel
		HAVING min(created_at) <= now() - make_interval(secs => $1) OR count(*) >= $2
		ORDER BY min(created_at) LIMIT 50`, s.cfg.DigestWindow.Seconds(), maxDigestItems))
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, g := range groups {
		if ctx.Err() != nil {
			break
		}
		did, err := s.deliverDigest(ctx, g.userID, g.channel)
		if err != nil {
			httpErrors.Inc("delivery")
			slog.Warn("delivery", "user", g.userID, "channel", g.channel, "err", err)
		}
		if did {
			sent++
		}
	}
	observeDeliveryQueue(ctx, s)
	return sent, nil
}

type digestItem struct {
	deliveryID int64
	attempts   int
	kind       string
	projectID  *string
	data       map[string]any
	createdAt  time.Time
}

// deliverDigest sends one person's pending rows on one channel as a single message. It reports
// whether a message was sent.
func (s *Server) deliverDigest(ctx context.Context, userID, channel string) (bool, error) {
	sent := false
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		items, err := collect(func(row pgx.Row) (digestItem, error) {
			var it digestItem
			var raw []byte
			err := row.Scan(&it.deliveryID, &it.attempts, &it.kind, &it.projectID, &raw, &it.createdAt)
			if err == nil {
				err = json.Unmarshal(raw, &it.data)
			}
			return it, err
		})(tx.Query(ctx, `
			SELECT d.id, d.attempts, n.kind, n.project_id, n.data, n.created_at
			FROM notification_deliveries d JOIN notifications n ON n.id = d.notification_id
			WHERE d.user_id = $1 AND d.channel = $2 AND d.status = 'pending' AND d.next_attempt_at <= now()
			ORDER BY d.id LIMIT $3 FOR UPDATE OF d SKIP LOCKED`, userID, channel, maxDigestItems))
		if err != nil || len(items) == 0 {
			return err // nothing left: another instance took it
		}
		ids := make([]int64, len(items))
		for k, it := range items {
			ids[k] = it.deliveryID
		}
		subject, body := s.renderDigest(items)
		sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
		sendErr := s.send(sendCtx, tx, userID, channel, subject, body)
		cancel()
		var permanent *permanentError
		switch {
		case sendErr == nil:
			sent = true
			_, err = tx.Exec(ctx, `UPDATE notification_deliveries SET status = 'sent', sent_at = now(), attempts = attempts + 1, last_error = NULL
				WHERE id = ANY($1)`, ids)
		case errors.As(sendErr, &permanent):
			_, err = tx.Exec(ctx, `UPDATE notification_deliveries SET status = 'skipped', last_error = $2 WHERE id = ANY($1)`, ids, sendErr.Error())
		default:
			// Back off 30s, 1m, 2m … capped at an hour; give up after MaxAttempts.
			_, err = tx.Exec(ctx, `UPDATE notification_deliveries SET attempts = attempts + 1, last_error = $2,
				status = CASE WHEN attempts + 1 >= $3 THEN 'failed' ELSE 'pending' END,
				next_attempt_at = now() + least(interval '1 hour', interval '30 seconds' * power(2, attempts))
				WHERE id = ANY($1)`, ids, truncate(sendErr.Error(), 500), s.cfg.DeliveryMaxAttempts)
			observeDeliveryFailure(channel)
		}
		return err
	})
	return sent, err
}

// permanentError means retrying can't help (no address, webhook removed): the rows are skipped.
type permanentError struct{ msg string }

func (e *permanentError) Error() string { return e.msg }

func (s *Server) send(ctx context.Context, tx pgx.Tx, userID, channel, subject, body string) error {
	switch channel {
	case "email":
		if s.mailer == nil {
			return &permanentError{"email is not configured"}
		}
		var to string
		if err := tx.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&to); err != nil {
			return err
		}
		return s.mailer.Send(ctx, to, subject, body)
	case "slack":
		var hook *string
		err := tx.QueryRow(ctx, `SELECT slack_webhook_url FROM notification_settings WHERE user_id = $1`, userID).Scan(&hook)
		if err == pgx.ErrNoRows || (err == nil && hook == nil) {
			return &permanentError{"Slack was disconnected"}
		}
		if err != nil {
			return err
		}
		if err := s.validSlackWebhook(*hook); err != nil {
			return &permanentError{"Slack webhook is no longer allowed"}
		}
		return s.postSlack(ctx, *hook, subject+"\n"+body)
	}
	return &permanentError{"unknown channel " + channel}
}

func (s *Server) postSlack(ctx context.Context, hook, text string) error {
	payload, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode < 300:
		return nil
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone || res.StatusCode == http.StatusForbidden:
		return &permanentError{"Slack rejected the webhook: " + res.Status}
	default:
		return fmt.Errorf("slack: %s", res.Status)
	}
}

// outboundClient never follows redirects, so an allowed host can't bounce requests elsewhere.
func outboundClient() *http.Client {
	return &http.Client{
		Timeout:       sendTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// renderDigest turns one or more notifications into a subject line and a plain-text body.
func (s *Server) renderDigest(items []digestItem) (string, string) {
	var b strings.Builder
	subject := ""
	if len(items) == 1 {
		subject = "Graphly: " + noticeText(items[0].kind, items[0].data)
	} else {
		subject = fmt.Sprintf("Graphly: %d updates", len(items))
	}
	for _, it := range items {
		b.WriteString("• " + noticeText(it.kind, it.data))
		if t, ok := it.data["title"].(string); ok && it.kind != kindLate {
			b.WriteString(" — " + t)
		}
		if link := s.link(it); link != "" {
			b.WriteString("\n  " + link)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nChange what you hear about: " + s.cfg.PublicURL + "/profile#notifications\n")
	return subject, b.String()
}

func (s *Server) link(it digestItem) string {
	if s.cfg.PublicURL == "" || it.projectID == nil {
		return ""
	}
	view := "board"
	if it.kind == kindLate || it.kind == kindDateMoved {
		view = "insights"
	}
	u := s.cfg.PublicURL + "/p/" + *it.projectID + "/" + view
	if k, ok := it.data["key"].(string); ok {
		u += "?issue=" + url.QueryEscape(k)
	}
	return u
}

// noticeText is one plain sentence per notification; web/src/components/Inbox.tsx says the same.
func noticeText(kind string, d map[string]any) string {
	str := func(k string) string {
		v, _ := d[k].(string)
		return v
	}
	num := func(k string) int {
		v, _ := d[k].(float64)
		return int(v)
	}
	who := str("actorName")
	if who == "" {
		who = "Someone"
	}
	switch kind {
	case kindUnblocked:
		return fmt.Sprintf("%s is unblocked: %s-%d %s is done", str("key"), str("projectKey"), num("byNumber"), str("byTitle"))
	case kindLate:
		issues, _ := d["issues"].([]any)
		if len(issues) == 1 {
			i, _ := issues[0].(map[string]any)
			n, _ := i["number"].(float64)
			late, _ := i["daysLate"].(float64)
			return fmt.Sprintf("%s-%d is now forecast %dd past its due date", str("projectKey"), int(n), int(late))
		}
		return fmt.Sprintf("%d of your issues are now forecast past their due dates", len(issues))
	case kindMention:
		where := ""
		if str("in") != "comment" {
			where = " (" + str("in") + ")"
		}
		return fmt.Sprintf("%s mentioned you on %s%s", who, str("key"), where)
	case kindStatus:
		return fmt.Sprintf("%s moved %s from %s to %s", who, str("key"), str("from"), str("to"))
	case kindDateMoved:
		to := str("to")
		if t, err := forecast.ParseDate(to); err == nil {
			to = t.Format("Jan 2")
		}
		sign := ""
		if num("days") > 0 {
			sign = "+"
		}
		return fmt.Sprintf("%s now ships %s (%s%d working days)", str("projectName"), to, sign, num("days"))
	}
	return kind
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---- SMTP ----

// smtpMailer sends through any SMTP server or provider relay (SendGrid, Postmark, SES, Mailgun…),
// upgrading to TLS with STARTTLS, or with implicit TLS on port 465.
type smtpMailer struct {
	host, port, username, password, from string
}

func newSMTPMailer(cfg Config) mailer {
	if cfg.SMTPHost == "" || cfg.SMTPFrom == "" {
		return nil
	}
	return &smtpMailer{host: cfg.SMTPHost, port: cfg.SMTPPort, username: cfg.SMTPUsername, password: cfg.SMTPPassword, from: cfg.SMTPFrom}
}

func (m *smtpMailer) Send(ctx context.Context, to, subject, body string) error {
	addr := net.JoinHostPort(m.host, m.port)
	d := net.Dialer{Timeout: sendTimeout}
	var conn net.Conn
	var err error
	if m.port == "465" {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: m.host})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: m.host}); err != nil {
			return err
		}
	}
	if m.username != "" {
		if err := c.Auth(smtp.PlainAuth("", m.username, m.password, m.host)); err != nil {
			return err
		}
	}
	if err := c.Mail(m.from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	msg := "From: " + m.from + "\r\nTo: " + to + "\r\nSubject: " + mimeHeader(subject) +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n" +
		"Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n\r\n" + strings.ReplaceAll(body, "\n", "\r\n")
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// mimeHeader keeps header values on one line and encodes non-ASCII per RFC 2047.
func mimeHeader(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	for _, r := range s {
		if r > 127 {
			return "=?utf-8?q?" + strings.NewReplacer("=", "=3D", "?", "=3F", "_", "=5F", " ", "_").Replace(qEncode(s)) + "?="
		}
	}
	return s
}

func qEncode(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c > 127 {
			b.WriteString("=" + strings.ToUpper(strconv.FormatInt(int64(c), 16)))
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}
