package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Lal-Jr/Graphly/server/forecast"
	"github.com/jackc/pgx/v5"
)

// Notifications are written in the same transaction as the change that caused them, so nobody is
// told about something that was rolled back. Delivery beyond the in-app inbox goes through the
// outbox in delivery.go, never inline in a request.

const (
	kindUnblocked = "unblocked"
	kindLate      = "late"
	kindMention   = "mention"
	kindStatus    = "status"
	kindDateMoved = "date_moved"
)

var notificationKinds = []string{kindUnblocked, kindLate, kindMention, kindStatus, kindDateMoved}

// channelPrefs says where one kind of notification goes.
type channelPrefs struct {
	InApp bool `json:"inApp"`
	Email bool `json:"email"`
	Slack bool `json:"slack"`
}

// defaultPrefs: everything in the inbox; email for things that need you; Slack for everything once
// you've given it a webhook. Watching status changes is the noisy one, so it stays off email.
var defaultPrefs = map[string]channelPrefs{
	kindUnblocked: {true, true, true},
	kindLate:      {true, true, true},
	kindMention:   {true, true, true},
	kindStatus:    {true, false, true},
	kindDateMoved: {true, true, true},
}

type notice struct {
	UserID    string
	ProjectID string
	IssueID   *string
	Kind      string
	ActorID   *string
	Data      map[string]any
	// Same key, same user: notified once.
	Dedupe string
}

// notify records a notification for one person, if they're still in the project's workspace and
// want this kind on some channel. It never notifies people about their own actions.
func (s *Server) notify(ctx context.Context, tx pgx.Tx, n notice) error {
	if n.ActorID != nil && *n.ActorID == n.UserID {
		return nil
	}
	prefs, err := s.userPrefs(ctx, tx, n.UserID)
	if err != nil {
		return err
	}
	p := prefs[n.Kind]
	if !p.InApp && !p.Email && !p.Slack {
		return nil
	}
	data := map[string]any{}
	for k, v := range n.Data {
		data[k] = v
	}
	// Snapshot what the notice is about, so it still reads right after renames and deletions.
	var wsID, projectKey, projectName string
	err = tx.QueryRow(ctx, `SELECT p.workspace_id, p.key, p.name FROM projects p
		JOIN workspace_members m ON m.workspace_id = p.workspace_id AND m.user_id = $2 WHERE p.id = $1`, n.ProjectID, n.UserID).
		Scan(&wsID, &projectKey, &projectName)
	if err == pgx.ErrNoRows {
		return nil // no longer a member
	}
	if err != nil {
		return err
	}
	data["projectKey"], data["projectName"] = projectKey, projectName
	if n.IssueID != nil {
		var num int
		var title string
		if err := tx.QueryRow(ctx, `SELECT number, title FROM issues WHERE id = $1`, *n.IssueID).Scan(&num, &title); err != nil {
			return err
		}
		data["key"] = projectKey + "-" + strconv.Itoa(num)
		data["title"] = title
	}
	if n.ActorID != nil {
		var name string
		if err := tx.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, *n.ActorID).Scan(&name); err == nil {
			data["actorName"] = name
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	var dedupe *string
	if n.Dedupe != "" {
		dedupe = &n.Dedupe
	}
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO notifications (user_id, workspace_id, project_id, issue_id, kind, actor_id, data, dedupe_key, inbox)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (user_id, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING RETURNING id`,
		n.UserID, wsID, n.ProjectID, n.IssueID, n.Kind, n.ActorID, raw, dedupe, p.InApp).Scan(&id)
	if err == pgx.ErrNoRows {
		return nil // already told them
	}
	if err != nil {
		return err
	}
	return s.enqueueDeliveries(ctx, tx, id, n.UserID, p)
}

func (s *Server) userPrefs(ctx context.Context, q querier, userID string) (map[string]channelPrefs, error) {
	out := map[string]channelPrefs{}
	for k, v := range defaultPrefs {
		out[k] = v
	}
	var raw []byte
	err := q.QueryRow(ctx, `SELECT prefs FROM notification_settings WHERE user_id = $1`, userID).Scan(&raw)
	if err == pgx.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var saved map[string]channelPrefs
	if json.Unmarshal(raw, &saved) == nil {
		for k, v := range saved {
			if _, known := out[k]; known {
				out[k] = v
			}
		}
	}
	return out, nil
}

// ---- watchers ----

func watch(ctx context.Context, tx pgx.Tx, issueID string, userIDs ...*string) error {
	for _, u := range userIDs {
		if u == nil {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO issue_watchers (issue_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, issueID, *u); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) setWatch(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	return s.inTx(r.Context(), func(tx pgx.Tx) error {
		if _, err := s.issueRole(r.Context(), tx, id, u, roleViewer); err != nil {
			return err
		}
		if r.Method == http.MethodDelete {
			_, err = tx.Exec(r.Context(), `DELETE FROM issue_watchers WHERE issue_id = $1 AND user_id = $2`, id, u.ID)
		} else {
			err = watch(r.Context(), tx, id, &u.ID)
		}
		if err != nil {
			return err
		}
		return s.writeWatch(w, r.Context(), tx, id, u.ID)
	})
}

func (s *Server) getWatch(w http.ResponseWriter, r *http.Request, u *User) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if _, err := s.issueRole(r.Context(), s.db, id, u, roleViewer); err != nil {
		return err
	}
	return s.writeWatch(w, r.Context(), s.db, id, u.ID)
}

func (s *Server) writeWatch(w http.ResponseWriter, ctx context.Context, q querier, issueID, userID string) error {
	var watching bool
	var count int
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM issue_watchers WHERE issue_id = $1 AND user_id = $2),
		(SELECT count(*) FROM issue_watchers WHERE issue_id = $1)`, issueID, userID).Scan(&watching, &count); err != nil {
		return err
	}
	return ok(w, map[string]any{"watching": watching, "watchers": count})
}

// ---- events ----

// noteStatusChange tells an issue's watchers it moved to another column. via names the automation
// (e.g. "GitHub") when there's no human actor.
func (s *Server) noteStatusChange(ctx context.Context, tx pgx.Tx, projectID string, before, after Issue, actorID *string, via string) error {
	var from, to string
	if err := tx.QueryRow(ctx, `SELECT (SELECT name FROM statuses WHERE id = $1), (SELECT name FROM statuses WHERE id = $2)`,
		before.StatusID, after.StatusID).Scan(&from, &to); err != nil {
		return err
	}
	watchers, err := collect(scanString)(tx.Query(ctx, `SELECT user_id FROM issue_watchers WHERE issue_id = $1`, after.ID))
	if err != nil {
		return err
	}
	data := map[string]any{"from": from, "to": to}
	if actorID == nil && via != "" {
		data["actorName"] = via
	}
	for _, uid := range watchers {
		if err := s.notify(ctx, tx, notice{
			UserID: uid, ProjectID: projectID, IssueID: &after.ID, Kind: kindStatus, ActorID: actorID,
			Data:   data,
			Dedupe: "status:" + after.ID + ":" + strconv.Itoa(after.Version),
		}); err != nil {
			return err
		}
	}
	return nil
}

// noteFreed tells the assignee of an issue that its last open blocker just finished.
func (s *Server) noteFreed(ctx context.Context, tx pgx.Tx, projectID, freedID, byID string, actorID *string) error {
	var assignee *string
	var byNum int
	var byTitle string
	if err := tx.QueryRow(ctx, `SELECT (SELECT assignee_id FROM issues WHERE id = $1), b.number, b.title FROM issues b WHERE b.id = $2`,
		freedID, byID).Scan(&assignee, &byNum, &byTitle); err != nil {
		return err
	}
	if assignee == nil {
		return nil
	}
	return s.notify(ctx, tx, notice{
		UserID: *assignee, ProjectID: projectID, IssueID: &freedID, Kind: kindUnblocked, ActorID: actorID,
		Data:   map[string]any{"byNumber": byNum, "byTitle": byTitle},
		Dedupe: "unblocked:" + freedID + ":" + byID + ":" + time.Now().UTC().Format("2006-01-02"),
	})
}

var mentionRe = regexp.MustCompile(`(?:^|[^\w@.])@([A-Za-z0-9][A-Za-z0-9._-]{0,63})`)

// mentionHandles maps the ways someone can be @mentioned to their user id: the part of their email
// before the @, their full name with spaces removed, and their first name when nobody else shares it.
func mentionHandles(members []Member) map[string]string {
	handles := map[string]string{}
	first := map[string][]string{}
	for _, m := range members {
		local, _, _ := strings.Cut(strings.ToLower(m.Email), "@")
		handles[local] = m.ID
		handles[strings.ToLower(strings.Join(strings.Fields(m.Name), ""))] = m.ID
		if f := strings.Fields(m.Name); len(f) > 0 {
			k := strings.ToLower(f[0])
			first[k] = append(first[k], m.ID)
		}
	}
	for k, ids := range first {
		if _, taken := handles[k]; !taken && len(ids) == 1 {
			handles[k] = ids[0]
		}
	}
	return handles
}

// mentionedIn returns the members @mentioned in text, in order of first mention.
func mentionedIn(text string, handles map[string]string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range mentionRe.FindAllStringSubmatch(text, -1) {
		h := strings.TrimRight(strings.ToLower(m[1]), "._-")
		if id, ok := handles[h]; ok && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// noteMentions notifies (and subscribes) people newly @mentioned in text. `previous` is the text
// before an edit, so re-saving a description doesn't re-notify everyone already mentioned in it.
func (s *Server) noteMentions(ctx context.Context, tx pgx.Tx, projectID, issueID, text, previous, where string, actorID *string, dedupe string) error {
	if !strings.Contains(text, "@") {
		return nil
	}
	var wsID string
	if err := tx.QueryRow(ctx, `SELECT workspace_id FROM projects WHERE id = $1`, projectID).Scan(&wsID); err != nil {
		return err
	}
	members, err := s.members(ctx, tx, wsID)
	if err != nil {
		return err
	}
	handles := mentionHandles(members)
	already := map[string]bool{}
	for _, id := range mentionedIn(previous, handles) {
		already[id] = true
	}
	excerpt := []rune(strings.TrimSpace(text))
	if len(excerpt) > 280 {
		excerpt = append(excerpt[:280], '…')
	}
	for _, uid := range mentionedIn(text, handles) {
		if already[uid] {
			continue
		}
		u := uid
		if err := watch(ctx, tx, issueID, &u); err != nil {
			return err
		}
		if err := s.notify(ctx, tx, notice{
			UserID: uid, ProjectID: projectID, IssueID: &issueID, Kind: kindMention, ActorID: actorID,
			Data: map[string]any{"in": where, "excerpt": string(excerpt)}, Dedupe: dedupe,
		}); err != nil {
			return err
		}
	}
	return nil
}

// forecastNotifications runs after the background job records a forecast: owners of issues that
// are newly forecast late hear about it (one notice per person per run, however many issues), and
// people involved in the project hear when the ship date has moved past the threshold since they
// were last told.
func (s *Server) forecastNotifications(ctx context.Context, prev, cur *ProjectForecast) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if prev != nil {
			wasLate := map[string]bool{}
			for _, l := range prev.Late {
				wasLate[l.ID] = true
			}
			byOwner := map[string][]lateIssue{}
			for _, l := range cur.Late {
				if !wasLate[l.ID] && l.AssigneeID != nil {
					byOwner[*l.AssigneeID] = append(byOwner[*l.AssigneeID], l)
				}
			}
			for owner, issues := range byOwner {
				ids := make([]string, len(issues))
				for k, l := range issues {
					ids[k] = l.ID
				}
				sort.Strings(ids)
				var issueID *string
				if len(issues) == 1 {
					issueID = &issues[0].ID
				}
				if err := s.notify(ctx, tx, notice{
					UserID: owner, ProjectID: cur.ProjectID, IssueID: issueID, Kind: kindLate,
					Data:   map[string]any{"issues": issues, "finish": cur.Finish},
					Dedupe: "late:" + cur.Today + ":" + shortHash(strings.Join(ids, ",")),
				}); err != nil {
					return err
				}
			}
		}

		var notified *time.Time
		if err := tx.QueryRow(ctx, `SELECT notified_finish FROM project_forecasts WHERE project_id = $1 FOR UPDATE`, cur.ProjectID).Scan(&notified); err != nil {
			return err
		}
		if cur.Finish == nil {
			return nil
		}
		now, _ := forecast.ParseDate(*cur.Finish)
		if notified == nil {
			// First reading: it's the baseline, not news.
			_, err := tx.Exec(ctx, `UPDATE project_forecasts SET notified_finish = $2 WHERE project_id = $1`, cur.ProjectID, now)
			return err
		}
		was := forecast.Day(*notified)
		moved := forecast.WorkdaysBetween(was, now) - forecast.WorkdaysBetween(now, was)
		if abs(moved) < s.cfg.ShipDateMoveThreshold {
			return nil
		}
		involved, err := collect(scanString)(tx.Query(ctx, `
			SELECT m.user_id FROM workspace_members m JOIN projects p ON p.workspace_id = m.workspace_id
			WHERE p.id = $1 AND (m.role = 'admin'
				OR EXISTS (SELECT 1 FROM issues i WHERE i.project_id = p.id AND (i.assignee_id = m.user_id OR i.reporter_id = m.user_id))
				OR EXISTS (SELECT 1 FROM issue_watchers w JOIN issues i ON i.id = w.issue_id WHERE i.project_id = p.id AND w.user_id = m.user_id))`,
			cur.ProjectID))
		if err != nil {
			return err
		}
		for _, uid := range involved {
			if err := s.notify(ctx, tx, notice{
				UserID: uid, ProjectID: cur.ProjectID, Kind: kindDateMoved,
				Data:   map[string]any{"from": forecast.FormatDate(was), "to": *cur.Finish, "days": moved, "p85": cur.FinishP85},
				Dedupe: "moved:" + cur.ProjectID + ":" + forecast.FormatDate(was) + ":" + *cur.Finish,
			}); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE project_forecasts SET notified_finish = $2 WHERE project_id = $1`, cur.ProjectID, now)
		return err
	})
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// ---- inbox ----

type Notification struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	ProjectID *string         `json:"projectId"`
	IssueID   *string         `json:"issueId"`
	ActorID   *string         `json:"actorId"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"createdAt"`
	ReadAt    *time.Time      `json:"readAt"`
}

// listNotifications returns the inbox newest first; ?before=<id> pages back.
func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request, u *User) error {
	before := int64(1<<63 - 1)
	if v := r.URL.Query().Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return badRequest("before must be a notification id")
		}
		before = n
	}
	items, err := collect(func(row pgx.Row) (Notification, error) {
		var n Notification
		return n, row.Scan(&n.ID, &n.Kind, &n.ProjectID, &n.IssueID, &n.ActorID, &n.Data, &n.CreatedAt, &n.ReadAt)
	})(s.db.Query(r.Context(), `SELECT id, kind, project_id, issue_id, actor_id, data, created_at, read_at FROM notifications
		WHERE user_id = $1 AND inbox AND id < $2 ORDER BY id DESC LIMIT 50`, u.ID, before))
	if err != nil {
		return err
	}
	var unread int
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM notifications WHERE user_id = $1 AND inbox AND read_at IS NULL`, u.ID).Scan(&unread); err != nil {
		return err
	}
	return ok(w, map[string]any{"items": items, "unread": unread})
}

func (s *Server) unreadCount(w http.ResponseWriter, r *http.Request, u *User) error {
	var unread int
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM notifications WHERE user_id = $1 AND inbox AND read_at IS NULL`, u.ID).Scan(&unread); err != nil {
		return err
	}
	return ok(w, map[string]int{"unread": unread})
}

// markRead marks the given notifications read, or all of them with {"all": true}.
func (s *Server) markRead(w http.ResponseWriter, r *http.Request, u *User) error {
	var body struct {
		IDs []int64 `json:"ids"`
		All bool    `json:"all"`
	}
	if err := readJSON(r, &body); err != nil {
		return err
	}
	if len(body.IDs) > 500 {
		return badRequest("at most 500 ids at a time")
	}
	var err error
	if body.All {
		_, err = s.db.Exec(r.Context(), `UPDATE notifications SET read_at = now() WHERE user_id = $1 AND read_at IS NULL`, u.ID)
	} else {
		_, err = s.db.Exec(r.Context(), `UPDATE notifications SET read_at = now() WHERE user_id = $1 AND id = ANY($2) AND read_at IS NULL`, u.ID, body.IDs)
	}
	if err != nil {
		return err
	}
	return s.unreadCount(w, r, u)
}

// ---- settings ----

type notificationSettings struct {
	Prefs map[string]channelPrefs `json:"prefs"`
	// Whether a Slack webhook is set; the URL itself is a secret and never sent back.
	SlackConnected bool `json:"slackConnected"`
	// Which channels this server can deliver on at all.
	EmailAvailable bool `json:"emailAvailable"`
}

func (s *Server) getNotificationSettings(w http.ResponseWriter, r *http.Request, u *User) error {
	prefs, err := s.userPrefs(r.Context(), s.db, u.ID)
	if err != nil {
		return err
	}
	var slack bool
	err = s.db.QueryRow(r.Context(), `SELECT slack_webhook_url IS NOT NULL FROM notification_settings WHERE user_id = $1`, u.ID).Scan(&slack)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	return ok(w, notificationSettings{Prefs: prefs, SlackConnected: slack, EmailAvailable: s.mailer != nil})
}

// updateNotificationSettings saves channel preferences, and optionally a Slack incoming webhook
// ("slackWebhookUrl": "" disconnects it).
func (s *Server) updateNotificationSettings(w http.ResponseWriter, r *http.Request, u *User) error {
	var body struct {
		Prefs           map[string]channelPrefs `json:"prefs"`
		SlackWebhookURL *string                 `json:"slackWebhookUrl"`
	}
	if err := readJSON(r, &body); err != nil {
		return err
	}
	current, err := s.userPrefs(r.Context(), s.db, u.ID)
	if err != nil {
		return err
	}
	for k, v := range body.Prefs {
		if _, known := current[k]; !known {
			return badRequest("unknown notification kind " + strconv.Quote(k))
		}
		current[k] = v
	}
	raw, _ := json.Marshal(current)
	err = s.inTx(r.Context(), func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `INSERT INTO notification_settings (user_id, prefs) VALUES ($1, $2)
			ON CONFLICT (user_id) DO UPDATE SET prefs = EXCLUDED.prefs, updated_at = now()`, u.ID, raw); err != nil {
			return err
		}
		if body.SlackWebhookURL == nil {
			return nil
		}
		var hook *string
		if v := strings.TrimSpace(*body.SlackWebhookURL); v != "" {
			if err := s.validSlackWebhook(v); err != nil {
				return err
			}
			hook = &v
		}
		_, err := tx.Exec(r.Context(), `UPDATE notification_settings SET slack_webhook_url = $2 WHERE user_id = $1`, u.ID, hook)
		return err
	})
	if err != nil {
		return err
	}
	return s.getNotificationSettings(w, r, u)
}

// validSlackWebhook only allows https URLs on the configured Slack hosts, so the delivery worker
// can't be pointed at internal services.
func (s *Server) validSlackWebhook(v string) error {
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.User != nil || len(v) > 500 {
		return badRequest("enter a Slack incoming webhook URL (https://hooks.slack.com/…)")
	}
	for _, h := range s.cfg.SlackWebhookHosts {
		if strings.EqualFold(u.Hostname(), h) && (u.Port() == "" || u.Port() == "443") {
			return nil
		}
	}
	return badRequest("Slack webhooks must be on " + strings.Join(s.cfg.SlackWebhookHosts, ", "))
}
