package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Lal-Jr/Graphly/server/forecast"
	"github.com/jackc/pgx/v5"
)

// Import from Jira and Linear. POST /api/workspaces/{ws}/import with {source, content, dryRun}.
//
// The whole import runs in one transaction. A dry run does all of it and rolls back, so the preview
// is exactly what a real run would do. Re-running an import updates what it created before
// (import_mappings) instead of duplicating it. Blocking links that would close a cycle, point
// outside the import, or cross projects are dropped and reported, never silently lost.

const (
	maxImportBytes  = 25 << 20
	maxImportIssues = 20000
	maxDropped      = 500
)

var errDryRun = errors.New("dry run")

type importRequest struct {
	Source  string `json:"source"`
	Content string `json:"content"`
	DryRun  bool   `json:"dryRun"`
}

type importProject struct {
	ID      string `json:"id,omitempty"`
	Key     string `json:"key"`
	Name    string `json:"name"`
	Action  string `json:"action"` // create | existing
	Created int    `json:"created"`
	Updated int    `json:"updated"`
}

type droppedLink struct {
	Blocker string `json:"blocker"`
	Blocked string `json:"blocked"`
	Reason  string `json:"reason"`
}

type createdStatus struct {
	Project  string `json:"project"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

type importReport struct {
	DryRun   bool            `json:"dryRun"`
	Source   string          `json:"source"`
	Format   string          `json:"format"`
	Projects []importProject `json:"projects"`
	Issues   struct {
		Total     int `json:"total"`
		Created   int `json:"created"`
		Updated   int `json:"updated"`
		Unchanged int `json:"unchanged"`
		Skipped   int `json:"skipped"`
	} `json:"issues"`
	Links struct {
		Total        int           `json:"total"`
		Created      int           `json:"created"`
		Existing     int           `json:"existing"`
		DroppedCount int           `json:"droppedCount"`
		Dropped      []droppedLink `json:"dropped"`
	} `json:"links"`
	Comments struct {
		Created  int `json:"created"`
		Existing int `json:"existing"`
	} `json:"comments"`
	StatusesCreated []createdStatus `json:"statusesCreated"`
	// Values with no Graphly equivalent, and how many issues had each.
	Unmapped struct {
		Types      map[string]int `json:"types"`
		Priorities map[string]int `json:"priorities"`
		People     map[string]int `json:"people"`
		Parents    int            `json:"parents"`
	} `json:"unmapped"`
	Warnings []string `json:"warnings"`
}

func (r *importReport) drop(blocker, blocked, reason string) {
	r.Links.DroppedCount++
	if len(r.Links.Dropped) < maxDropped {
		r.Links.Dropped = append(r.Links.Dropped, droppedLink{blocker, blocked, reason})
	}
}

func (s *Server) importHandler(w http.ResponseWriter, r *http.Request, u *User) error {
	wsID, err := pathID(r, "ws")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errStatus(http.StatusUnsupportedMediaType, "expected application/json")
	}
	var req importRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxImportBytes+4096)).Decode(&req); err != nil {
		return badRequest("invalid JSON body")
	}
	if len(req.Content) > maxImportBytes {
		return errStatus(http.StatusRequestEntityTooLarge, "exports are limited to 25 MB")
	}
	if err := s.requireAdmin(r.Context(), wsID, u.ID); err != nil {
		return err
	}
	p, format, err := parseExport(req.Source, []byte(req.Content))
	if err != nil {
		return err
	}
	if len(p.issues) == 0 {
		return badRequest("the export has no issues")
	}
	if len(p.issues) > maxImportIssues {
		return badRequest(fmt.Sprintf("imports are limited to %d issues; split the export by project", maxImportIssues))
	}
	var rep *importReport
	err = s.inTx(r.Context(), func(tx pgx.Tx) error {
		rep, err = s.runImport(r.Context(), tx, wsID, u.ID, req.Source, format, p)
		if err != nil {
			return err
		}
		rep.DryRun = req.DryRun
		if req.DryRun {
			return errDryRun
		}
		raw, _ := json.Marshal(rep)
		_, err = tx.Exec(r.Context(), `INSERT INTO imports (workspace_id, user_id, source, format, report) VALUES ($1, $2, $3, $4, $5)`,
			wsID, u.ID, req.Source, format, raw)
		if err == nil {
			err = s.audit(r.Context(), tx, wsID, u.ID, "import", "workspace", wsID, map[string]any{
				"source": req.Source, "issues": rep.Issues, "projects": len(rep.Projects), "linksDropped": rep.Links.DroppedCount})
		}
		return err
	})
	if err != nil && !errors.Is(err, errDryRun) {
		return err
	}
	return ok(w, rep)
}

type importer struct {
	s        *Server
	ctx      context.Context
	tx       pgx.Tx
	ws, user string
	source   string
	rep      *importReport
	members  []Member
	byEmail  map[string]string
	byName   map[string]string
	projects map[string]*importTarget // source project key → target
	issueIDs map[string]string        // ext id → issue id (this import and earlier ones)
	issuePrj map[string]string        // issue id → project id
	maxSrc   map[string]int           // project key → highest source issue number in this export
}

type importTarget struct {
	report   *importProject
	id, key  string
	statuses map[string]Status // lower-cased name → status
	nextPos  int
	numbers  map[int]bool
	maxNum   int
	rank     float64
}

func (s *Server) runImport(ctx context.Context, tx pgx.Tx, wsID, userID, source, format string, p parsed) (*importReport, error) {
	rep := &importReport{Source: source, Format: format, Projects: []importProject{}, Warnings: append([]string{}, p.warnings...)}
	rep.Links.Dropped = []droppedLink{}
	rep.StatusesCreated = []createdStatus{}
	rep.Unmapped.Types, rep.Unmapped.Priorities, rep.Unmapped.People = map[string]int{}, map[string]int{}, map[string]int{}
	// One import per workspace and source at a time, so two runs can't both create the same rows.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('import:' || $1 || ':' || $2))`, wsID, source); err != nil {
		return nil, err
	}
	members, err := s.members(ctx, tx, wsID)
	if err != nil {
		return nil, err
	}
	im := &importer{s: s, maxSrc: map[string]int{}, ctx: ctx, tx: tx, ws: wsID, user: userID, source: source, rep: rep, members: members,
		byEmail: map[string]string{}, byName: map[string]string{}, projects: map[string]*importTarget{},
		issueIDs: map[string]string{}, issuePrj: map[string]string{}}
	dupName := map[string]bool{}
	for _, m := range members {
		im.byEmail[strings.ToLower(m.Email)] = m.ID
		n := strings.ToLower(strings.TrimSpace(m.Name))
		if _, seen := im.byName[n]; seen {
			dupName[n] = true
		}
		im.byName[n] = m.ID
	}
	for n := range dupName {
		delete(im.byName, n) // ambiguous names don't match anyone
	}
	if err := im.loadMappings(); err != nil {
		return nil, err
	}

	// Drop rows without an id and duplicate ids (last one wins, as in a re-export).
	seen := map[string]int{}
	var issues []extIssue
	for _, is := range p.issues {
		if is.ExtID == "" || strings.TrimSpace(is.Title) == "" {
			rep.Issues.Skipped++
			continue
		}
		if k, ok := seen[is.ExtID]; ok {
			issues[k] = is
			continue
		}
		seen[is.ExtID] = len(issues)
		issues = append(issues, is)
	}
	if rep.Issues.Skipped > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d rows had no key or title and were skipped.", rep.Issues.Skipped))
	}
	rep.Issues.Total = len(issues)
	for _, is := range issues {
		if m := issueKeyRe.FindStringSubmatch(is.ExtID); m != nil {
			n, _ := strconv.Atoi(m[2])
			k := projectKey(m[1], "")
			im.maxSrc[k] = max(im.maxSrc[k], n)
		}
	}

	// Epics first so children can point at them; otherwise keep the export's order (it becomes the rank).
	sort.SliceStable(issues, func(a, b int) bool { return mapType(issues[a].Type) == "epic" && mapType(issues[b].Type) != "epic" })
	touched := map[string]bool{}
	var ids []string
	for _, is := range issues {
		id, err := im.upsertIssue(is)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", is.ExtID, err)
		}
		touched[im.issuePrj[id]] = true
		ids = append(ids, id)
	}
	if err := im.setParents(issues); err != nil {
		return nil, err
	}
	if err := im.linkAll(issues, touched); err != nil {
		return nil, err
	}
	for _, is := range issues {
		if err := im.comments(is); err != nil {
			return nil, err
		}
	}
	for _, t := range im.projects {
		if _, err := tx.Exec(ctx, `UPDATE projects SET issue_seq = GREATEST(issue_seq, (SELECT coalesce(max(number), 0) FROM issues WHERE project_id = $1)) WHERE id = $1`, t.id); err != nil {
			return nil, err
		}
	}
	if err := s.syncLifecycle(ctx, tx, ids); err != nil {
		return nil, err
	}
	for pid := range touched {
		if err := logActivity(ctx, tx, pid, nil, userID, "import", map[string]any{"source": source}); err != nil {
			return nil, err
		}
		// Clients reload rather than replaying thousands of upserts.
		if err := publish(ctx, tx, pid, "project.reload", map[string]any{}); err != nil {
			return nil, err
		}
	}
	for _, t := range im.projects {
		rep.Projects = append(rep.Projects, *t.report)
	}
	slices.SortFunc(rep.Projects, func(a, b importProject) int { return strings.Compare(a.Key, b.Key) })
	return rep, nil
}

func (im *importer) loadMappings() error {
	rows, err := im.tx.Query(im.ctx, `SELECT m.external_id, i.id, i.project_id FROM import_mappings m JOIN issues i ON i.id = m.target_id
		WHERE m.workspace_id = $1 AND m.source = $2 AND m.kind = 'issue'`, im.ws, im.source)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ext, id, pid string
		if err := rows.Scan(&ext, &id, &pid); err != nil {
			return err
		}
		im.issueIDs[ext], im.issuePrj[id] = id, pid
	}
	return rows.Err()
}

func (im *importer) mapping(kind, ext, target string) error {
	_, err := im.tx.Exec(im.ctx, `INSERT INTO import_mappings (workspace_id, source, kind, external_id, target_id) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (workspace_id, source, kind, external_id) DO UPDATE SET target_id = EXCLUDED.target_id, imported_at = now()`,
		im.ws, im.source, kind, ext, target)
	return err
}

var nonKeyRe = regexp.MustCompile(`[^A-Z0-9]`)

// projectKey turns a source key into a valid Graphly key (2–10 letters/digits, starting with a letter).
func projectKey(src, name string) string {
	k := nonKeyRe.ReplaceAllString(strings.ToUpper(src), "")
	if k == "" || k[0] < 'A' || k[0] > 'Z' {
		k = nonKeyRe.ReplaceAllString(strings.ToUpper(name), "")
	}
	for len(k) > 0 && (k[0] < 'A' || k[0] > 'Z') {
		k = k[1:]
	}
	if len(k) > 10 {
		k = k[:10]
	}
	for len(k) < 2 {
		k += "X"
	}
	return k
}

func (im *importer) target(is extIssue) (*importTarget, error) {
	src := firstNonEmpty(is.ProjectKey, "IMPORT")
	if t, ok := im.projects[src]; ok {
		return t, nil
	}
	ctx, tx := im.ctx, im.tx
	t := &importTarget{statuses: map[string]Status{}, numbers: map[int]bool{}}
	var pid string
	err := tx.QueryRow(ctx, `SELECT p.id FROM import_mappings m JOIN projects p ON p.id = m.target_id
		WHERE m.workspace_id = $1 AND m.source = $2 AND m.kind = 'project' AND m.external_id = $3`, im.ws, im.source, src).Scan(&pid)
	if errors.Is(err, pgx.ErrNoRows) {
		key := projectKey(src, is.ProjectName)
		err = tx.QueryRow(ctx, `SELECT id FROM projects WHERE workspace_id = $1 AND key = $2`, im.ws, key).Scan(&pid)
		if errors.Is(err, pgx.ErrNoRows) {
			name := strings.TrimSpace(firstNonEmpty(is.ProjectName, key))
			if utf8.RuneCountInString(name) > 80 {
				name = string([]rune(name)[:80])
			}
			p, err := createProjectTx(ctx, tx, im.ws, key, name, "Imported from "+sourceName(im.source)+".", false, nil)
			if err != nil {
				return nil, err
			}
			pid = p.ID
			t.report = &importProject{ID: p.ID, Key: key, Name: name, Action: "create"}
		} else if err != nil {
			return nil, err
		}
		if err := im.mapping("project", src, pid); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	t.id = pid
	// Serialize with link edits (addBlocker) so the cycle check below stays valid.
	var name string
	if err := tx.QueryRow(ctx, `SELECT key, name FROM projects WHERE id = $1 FOR UPDATE`, pid).Scan(&t.key, &name); err != nil {
		return nil, err
	}
	if t.report == nil {
		t.report = &importProject{ID: pid, Key: t.key, Name: name, Action: "existing"}
	}
	sts, err := im.s.statuses(ctx, tx, pid)
	if err != nil {
		return nil, err
	}
	for _, st := range sts {
		t.statuses[strings.ToLower(st.Name)] = st
		t.nextPos = max(t.nextPos, st.Position+1)
	}
	rows, err := tx.Query(ctx, `SELECT number FROM issues WHERE project_id = $1`, pid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		t.numbers[n] = true
		t.maxNum = max(t.maxNum, n)
	}
	rows.Close()
	var seq int
	if err := tx.QueryRow(ctx, `SELECT issue_seq FROM projects WHERE id = $1`, pid).Scan(&seq); err != nil {
		return nil, err
	}
	t.maxNum = max(t.maxNum, seq)
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(rank), 0) FROM issues WHERE project_id = $1`, pid).Scan(&t.rank); err != nil {
		return nil, err
	}
	im.projects[src] = t
	return t, nil
}

func sourceName(s string) string {
	if s == "linear" {
		return "Linear"
	}
	return "Jira"
}

func mapType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "bug", "defect", "incident", "problem":
		return "bug"
	case "story", "user story", "feature", "improvement", "new feature":
		return "story"
	case "epic":
		return "epic"
	case "task", "sub-task", "subtask", "issue", "spike", "chore", "technical task":
		return "task"
	}
	return ""
}

func mapPriority(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "highest", "blocker", "critical", "urgent", "p0", "p1":
		return "highest"
	case "high", "major", "p2":
		return "high"
	case "medium", "normal", "p3", "", "no priority", "none":
		return "medium"
	case "low", "minor", "p4":
		return "low"
	case "lowest", "trivial", "p5":
		return "lowest"
	}
	return ""
}

// guessCategory places a status we haven't seen by its name.
func guessCategory(name string) string {
	n := strings.ToLower(name)
	for _, w := range []string{"done", "closed", "resolved", "complete", "released", "shipped", "cancel", "won't", "wont", "duplicate"} {
		if strings.Contains(n, w) {
			return "done"
		}
	}
	for _, w := range []string{"progress", "review", "doing", "started", "testing", "qa", "development", "active"} {
		if strings.Contains(n, w) {
			return "in_progress"
		}
	}
	return "todo"
}

func (im *importer) person(p person) *string {
	if p.empty() {
		return nil
	}
	if id, ok := im.byEmail[strings.ToLower(p.Email)]; ok && p.Email != "" {
		return &id
	}
	if id, ok := im.byName[strings.ToLower(strings.TrimSpace(p.Name))]; ok && p.Name != "" {
		return &id
	}
	im.rep.Unmapped.People[p.String()]++
	return nil
}

func (im *importer) status(t *importTarget, name, category string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = map[string]string{"in_progress": "In Progress", "done": "Done"}[category]
		if name == "" {
			name = "To Do"
		}
	}
	if st, ok := t.statuses[strings.ToLower(name)]; ok {
		return st.ID, nil
	}
	if category == "" {
		category = guessCategory(name)
	}
	if utf8.RuneCountInString(name) > 40 {
		name = string([]rune(name)[:40])
	}
	var st Status
	if err := im.tx.QueryRow(im.ctx, `INSERT INTO statuses (project_id, name, category, position) VALUES ($1, $2, $3, $4)
		RETURNING id, project_id, name, category, position`, t.id, name, category, t.nextPos).Scan(&st.ID, &st.ProjectID, &st.Name, &st.Category, &st.Position); err != nil {
		return "", err
	}
	t.nextPos++
	t.statuses[strings.ToLower(name)] = st
	im.rep.StatusesCreated = append(im.rep.StatusesCreated, createdStatus{t.key, name, category})
	return st.ID, nil
}

func cleanLabels(in []string) []string {
	out := []string{}
	for _, l := range in {
		l = strings.ToLower(strings.Join(strings.Fields(l), "-"))
		if l != "" && len(l) <= 40 && !slices.Contains(out, l) && len(out) < 20 {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func (im *importer) upsertIssue(is extIssue) (string, error) {
	t, err := im.target(is)
	if err != nil {
		return "", err
	}
	typ := mapType(is.Type)
	if typ == "" {
		im.rep.Unmapped.Types[is.Type]++
		typ = "task"
	}
	prio := mapPriority(is.Priority)
	if prio == "" {
		im.rep.Unmapped.Priorities[is.Priority]++
		prio = "medium"
	}
	statusID, err := im.status(t, is.Status, is.Category)
	if err != nil {
		return "", err
	}
	est := 0.0 // no estimate stays visibly unestimated rather than defaulting to 1 day
	if is.Estimate != nil && *is.Estimate > 0 && !math.IsNaN(*is.Estimate) {
		est = math.Min(1000, math.Round(*is.Estimate*100)/100)
	}
	var due *string
	if is.DueDate != "" {
		due = &is.DueDate
	}
	title := clip(strings.Join(strings.Fields(is.Title), " "), 255)
	desc := clip(is.Description, 50000)
	assignee, reporter := im.person(is.Assignee), im.person(is.Reporter)
	labels := cleanLabels(is.Labels)

	if id, ok := im.issueIDs[is.ExtID]; ok && im.issuePrj[id] == t.id {
		tag, err := im.tx.Exec(im.ctx, `UPDATE issues SET type = $2, title = $3, description = $4, status_id = $5, priority = $6,
				assignee_id = $7, reporter_id = $8, estimate = $9, due_date = $10::date, labels = $11, version = version + 1, updated_at = now()
			WHERE id = $1 AND (type, title, description, status_id, priority, assignee_id, reporter_id, estimate, due_date, labels)
				IS DISTINCT FROM ($2, $3, $4, $5::uuid, $6, $7::uuid, $8::uuid, $9::float8, $10::date, $11::text[])`,
			id, typ, title, desc, statusID, prio, assignee, reporter, est, due, labels)
		if err != nil {
			return "", err
		}
		if tag.RowsAffected() > 0 {
			im.rep.Issues.Updated++
			t.report.Updated++
		} else {
			im.rep.Issues.Unchanged++
		}
		return id, nil
	}

	// Keep the source number (ENG-42 stays 42) when the key matches and the number is free.
	number := 0
	if m := issueKeyRe.FindStringSubmatch(is.ExtID); m != nil && strings.EqualFold(projectKey(m[1], ""), t.key) {
		if n, err := strconv.Atoi(m[2]); err == nil && n > 0 && n < 1<<30 && !t.numbers[n] {
			number = n
		}
	}
	if number == 0 {
		// Above every number in use and every number a later row may keep.
		number = max(t.maxNum, im.maxSrc[t.key]) + 1
	}
	t.numbers[number] = true
	t.maxNum = max(t.maxNum, number)
	t.rank += 1024
	created := time.Now()
	if is.Created != nil {
		created = *is.Created
	}
	var id string
	if err := im.tx.QueryRow(im.ctx, `INSERT INTO issues (project_id, number, type, title, description, status_id, priority, assignee_id,
			reporter_id, estimate, due_date, labels, rank, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::date, $12, $13, $14, now()) RETURNING id`,
		t.id, number, typ, title, desc, statusID, prio, assignee, reporter, est, due, labels, t.rank, created).Scan(&id); err != nil {
		return "", err
	}
	if _, err := im.tx.Exec(im.ctx, `UPDATE projects SET issue_seq = GREATEST(issue_seq, $2) WHERE id = $1`, t.id, number); err != nil {
		return "", err
	}
	if err := watch(im.ctx, im.tx, id, assignee, reporter); err != nil {
		return "", err
	}
	if err := im.mapping("issue", is.ExtID, id); err != nil {
		return "", err
	}
	im.issueIDs[is.ExtID], im.issuePrj[id] = id, t.id
	im.rep.Issues.Created++
	t.report.Created++
	return id, nil
}

// setParents points issues at their epic. Parents that aren't epics (Jira sub-tasks, Linear
// sub-issues) have no Graphly equivalent and are counted as unmapped.
func (im *importer) setParents(issues []extIssue) error {
	for _, is := range issues {
		if is.Parent == "" || mapType(is.Type) == "epic" {
			continue
		}
		id := im.issueIDs[is.ExtID]
		pid, found := im.issueIDs[is.Parent]
		var epic bool
		if found {
			if err := im.tx.QueryRow(im.ctx, `SELECT EXISTS (SELECT 1 FROM issues e JOIN issues c ON c.project_id = e.project_id
				WHERE e.id = $1 AND c.id = $2 AND e.type = 'epic')`, pid, id).Scan(&epic); err != nil {
				return err
			}
		}
		if !epic {
			im.rep.Unmapped.Parents++
			continue
		}
		if _, err := im.tx.Exec(im.ctx, `UPDATE issues SET epic_id = $2, version = version + 1, updated_at = now()
			WHERE id = $1 AND epic_id IS DISTINCT FROM $2::uuid`, id, pid); err != nil {
			return err
		}
	}
	return nil
}

// linkAll adds every blocking link it can. It walks links in export order and drops any that would
// close a cycle with what's already in the project, so the result is a DAG and the report names
// exactly which links were left out.
func (im *importer) linkAll(issues []extIssue, touched map[string]bool) error {
	type edge struct{ blocker, blocked string }
	var cands []edge
	seen := map[edge]bool{}
	add := func(blocker, blocked string) {
		e := edge{blocker, blocked}
		if !seen[e] {
			seen[e] = true
			cands = append(cands, e)
		}
	}
	for _, is := range issues {
		for _, b := range is.Blocks {
			add(is.ExtID, b)
		}
		for _, b := range is.BlockedBy {
			add(b, is.ExtID)
		}
	}
	im.rep.Links.Total = len(cands)
	if len(cands) == 0 {
		return nil
	}
	// Existing links in the touched projects, as adjacency blocker → blocked.
	out := map[string][]string{}
	exists := map[[2]string]bool{}
	var pids []string
	for p := range touched {
		pids = append(pids, p)
	}
	rows, err := im.tx.Query(im.ctx, `SELECT l.blocker_id, l.blocked_id FROM issue_links l JOIN issues i ON i.id = l.blocked_id WHERE i.project_id = ANY($1)`, pids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a, b string
		if err := rows.Scan(&a, &b); err != nil {
			rows.Close()
			return err
		}
		out[a] = append(out[a], b)
		exists[[2]string{a, b}] = true
	}
	rows.Close()

	type resolved struct {
		edge
		from, to string
	}
	var ok []resolved
	for _, e := range cands {
		from, f := im.issueIDs[e.blocker]
		to, g := im.issueIDs[e.blocked]
		switch {
		case e.blocker == e.blocked:
			im.rep.drop(e.blocker, e.blocked, "an issue can't block itself")
		case !f || !g:
			missing := e.blocker
			if f {
				missing = e.blocked
			}
			im.rep.drop(e.blocker, e.blocked, missing+" isn't in this import")
		case im.issuePrj[from] != im.issuePrj[to]:
			im.rep.drop(e.blocker, e.blocked, "the issues are in different projects")
		case exists[[2]string{from, to}]:
			im.rep.Links.Existing++
		default:
			ok = append(ok, resolved{e, from, to})
		}
	}
	// Only links whose ends are both on some cycle of the full candidate graph can close one; the
	// rest go straight in. That keeps the per-link reachability search to the few that need it.
	var nodes []forecast.Node
	deps := map[string][]string{}
	for a, bs := range out {
		for _, b := range bs {
			deps[b] = append(deps[b], a)
		}
	}
	for _, r := range ok {
		deps[r.to] = append(deps[r.to], r.from)
	}
	all := map[string]bool{}
	for b, as := range deps {
		all[b] = true
		for _, a := range as {
			all[a] = true
		}
	}
	for id := range all {
		nodes = append(nodes, forecast.Node{ID: id, Estimate: 1, State: "todo", DependsOn: deps[id]})
	}
	cyclic := forecast.FindCycles(nodes)

	reaches := func(from, to string) bool {
		stack, visited := []string{from}, map[string]bool{from: true}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if n == to {
				return true
			}
			for _, m := range out[n] {
				if !visited[m] {
					visited[m] = true
					stack = append(stack, m)
				}
			}
		}
		return false
	}
	var from, to []string
	for _, r := range ok {
		// blocker → blocked closes a cycle if blocked already (transitively) blocks blocker.
		if cyclic[r.from] && cyclic[r.to] && reaches(r.to, r.from) {
			im.rep.drop(r.blocker, r.blocked, "it would create a dependency cycle")
			continue
		}
		out[r.from] = append(out[r.from], r.to)
		from, to = append(from, r.from), append(to, r.to)
	}
	if len(from) > 0 {
		tag, err := im.tx.Exec(im.ctx, `INSERT INTO issue_links (blocker_id, blocked_id) SELECT * FROM unnest($1::uuid[], $2::uuid[]) ON CONFLICT DO NOTHING`, from, to)
		if err != nil {
			return err
		}
		im.rep.Links.Created = int(tag.RowsAffected())
	}
	return nil
}

func (im *importer) comments(is extIssue) error {
	issueID := im.issueIDs[is.ExtID]
	for _, c := range is.Comments {
		body := strings.TrimSpace(c.Body)
		if body == "" {
			continue
		}
		ext := c.ExtID
		if ext == "" {
			// CSV comments have no stable id: identify them by content.
			h := sha256.Sum256([]byte(is.ExtID + "\x00" + c.Author.String() + "\x00" + body))
			ext = is.ExtID + "#" + hex.EncodeToString(h[:12])
		}
		var exists bool
		if err := im.tx.QueryRow(im.ctx, `SELECT EXISTS (SELECT 1 FROM import_mappings m JOIN comments c ON c.id = m.target_id
			WHERE m.workspace_id = $1 AND m.source = $2 AND m.kind = 'comment' AND m.external_id = $3)`, im.ws, im.source, ext).Scan(&exists); err != nil {
			return err
		}
		if exists {
			im.rep.Comments.Existing++
			continue
		}
		author := im.person(c.Author)
		if author == nil && !c.Author.empty() {
			body = "**" + c.Author.String() + "** (imported): " + body
		}
		created := time.Now()
		if c.Created != nil {
			created = *c.Created
		}
		var id string
		if err := im.tx.QueryRow(im.ctx, `INSERT INTO comments (issue_id, author_id, body, created_at) VALUES ($1, $2, $3, $4) RETURNING id`,
			issueID, author, clip(body, 20000), created).Scan(&id); err != nil {
			return err
		}
		if err := im.mapping("comment", ext, id); err != nil {
			return err
		}
		im.rep.Comments.Created++
	}
	return nil
}
