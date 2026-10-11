package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Parsers for Jira and Linear exports. Each turns one export into extIssues; the importer does the
// mapping onto Graphly. Shapes follow the vendors' documented exports:
//   - Jira CSV: Issues → Export → CSV (all fields). Repeated columns (Labels, Comment, issue links)
//     are read as lists.
//   - Jira JSON: the REST search response ({"issues": [{key, fields}]}), or a bare array of issues.
//   - Linear CSV: Settings → Import/Export → Export CSV.
//   - Linear JSON: a GraphQL issues query ({"data": {"issues": {"nodes": [...]}}}), or a bare array.

type person struct{ Email, Name string }

func (p person) empty() bool { return p.Email == "" && p.Name == "" }

func (p person) String() string {
	if p.Name != "" {
		return p.Name
	}
	return p.Email
}

type extComment struct {
	ExtID   string
	Author  person
	Body    string
	Created *time.Time
}

type extIssue struct {
	ExtID       string // the source's issue key, e.g. ENG-12
	ProjectKey  string
	ProjectName string
	Type        string // the source's type name
	Title       string
	Description string
	Status      string
	Category    string // todo | in_progress | done, or "" to guess from the status name
	Priority    string // the source's priority name
	Assignee    person
	Reporter    person
	Labels      []string
	DueDate     string
	Estimate    *float64
	Parent      string // ext id of the parent (Jira epic or parent issue)
	Created     *time.Time
	Comments    []extComment
	Blocks      []string // ext ids this issue blocks
	BlockedBy   []string
}

type parsed struct {
	issues   []extIssue
	warnings []string
}

var issueKeyRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*)-(\d+)$`)

// keyPrefix returns "ENG" for "ENG-12".
func keyPrefix(k string) string {
	if m := issueKeyRe.FindStringSubmatch(k); m != nil {
		return m[1]
	}
	return ""
}

func parseExport(source string, content []byte) (parsed, string, error) {
	trimmed := bytes.TrimLeft(content, " \t\r\n\ufeff")
	trimmed = bytes.TrimPrefix(trimmed, []byte("\xef\xbb\xbf"))
	isJSON := len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
	format := "csv"
	if isJSON {
		format = "json"
	}
	var p parsed
	var err error
	switch {
	case source == "jira" && isJSON:
		p, err = parseJiraJSON(trimmed)
	case source == "jira":
		p, err = parseJiraCSV(trimmed)
	case source == "linear" && isJSON:
		p, err = parseLinearJSON(trimmed)
	case source == "linear":
		p, err = parseLinearCSV(trimmed)
	default:
		return p, format, badRequest("source must be jira or linear")
	}
	return p, format, err
}

// ---- CSV helpers ----

type csvTable struct {
	cols map[string][]int // lower-cased header → column indexes (Jira repeats columns)
	rows [][]string
}

func readCSV(content []byte) (*csvTable, error) {
	r := csv.NewReader(bytes.NewReader(content))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	header, err := r.Read()
	if err != nil {
		return nil, badRequest("couldn't read the CSV header")
	}
	t := &csvTable{cols: map[string][]int{}}
	for i, h := range header {
		k := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
		t.cols[k] = append(t.cols[k], i)
	}
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, badRequest("invalid CSV: " + err.Error())
		}
		t.rows = append(t.rows, row)
	}
	return t, nil
}

func (t *csvTable) has(name string) bool { return len(t.cols[name]) > 0 }

// get returns the first non-empty value among the named columns.
func (t *csvTable) get(row []string, names ...string) string {
	for _, n := range names {
		for _, i := range t.cols[n] {
			if i < len(row) && strings.TrimSpace(row[i]) != "" {
				return strings.TrimSpace(row[i])
			}
		}
	}
	return ""
}

// all returns every non-empty value of a repeated column.
func (t *csvTable) all(row []string, name string) []string {
	var out []string
	for _, i := range t.cols[name] {
		if i < len(row) && strings.TrimSpace(row[i]) != "" {
			out = append(out, strings.TrimSpace(row[i]))
		}
	}
	return out
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' }) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

var dateLayouts = []string{
	"2006-01-02", time.RFC3339, "2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05.000Z0700", "2006-01-02 15:04:05",
	"2006-01-02 15:04", "02/Jan/06 3:04 PM", "02/Jan/06 15:04", "02/Jan/06", "2/Jan/06 3:04 PM", "2/Jan/06",
	"Mon Jan 02 2006 15:04:05 GMT-0700", "1/2/2006", "1/2/2006 15:04", "Jan 2, 2006",
}

func parseTime(v string) *time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	// Linear: "Mon Jan 15 2024 10:00:00 GMT+0000 (Coordinated Universal Time)"
	if i := strings.Index(v, " ("); i > 0 {
		v = v[:i]
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, v); err == nil {
			return &t
		}
	}
	return nil
}

func dateOnly(v string) string {
	if t := parseTime(v); t != nil {
		return t.Format("2006-01-02")
	}
	return ""
}

func parseNumber(v string) *float64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil
	}
	return &f
}

const secondsPerDay = 8 * 3600

// ---- Jira ----

func jiraCategory(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "new", "to do", "todo":
		return "todo"
	case "indeterminate", "in progress":
		return "in_progress"
	case "done", "complete":
		return "done"
	}
	return ""
}

var jiraAccountRe = regexp.MustCompile(`^[0-9a-fA-F:\-]{10,}$`)

// Jira CSV comment cells look like "15/Mar/24 9:01 AM;557058:f1e2...;Body text".
func jiraCSVComment(cell string) extComment {
	c := extComment{Body: cell}
	parts := strings.SplitN(cell, ";", 3)
	if len(parts) == 3 {
		if t := parseTime(parts[0]); t != nil {
			c.Created, c.Body = t, parts[2]
			if strings.Contains(parts[1], "@") {
				c.Author.Email = parts[1]
			} else if !jiraAccountRe.MatchString(parts[1]) {
				c.Author.Name = parts[1]
			}
		}
	}
	return c // no stable id in CSV: the importer identifies it by content
}

func parseJiraCSV(content []byte) (parsed, error) {
	t, err := readCSV(content)
	if err != nil {
		return parsed{}, err
	}
	if !t.has("issue key") || !t.has("summary") {
		return parsed{}, badRequest("this doesn't look like a Jira CSV export (no Issue key/Summary columns)")
	}
	var p parsed
	idToKey := map[string]string{}
	for _, row := range t.rows {
		idToKey[t.get(row, "issue id")] = t.get(row, "issue key")
	}
	pointCols := []string{"custom field (story points)", "custom field (story point estimate)", "story points", "story point estimate"}
	if !t.has("custom field (story points)") && !t.has("custom field (story point estimate)") && !t.has("story points") && !t.has("original estimate") {
		p.warnings = append(p.warnings, "No Story Points or Original Estimate column: imported issues will be unestimated.")
	}
	for _, row := range t.rows {
		key := t.get(row, "issue key")
		if key == "" {
			continue
		}
		is := extIssue{
			ExtID:       key,
			ProjectKey:  firstNonEmpty(t.get(row, "project key"), keyPrefix(key)),
			ProjectName: t.get(row, "project name"),
			Type:        t.get(row, "issue type"),
			Title:       t.get(row, "summary"),
			Description: t.get(row, "description"),
			Status:      t.get(row, "status"),
			Category:    jiraCategory(t.get(row, "status category")),
			Priority:    t.get(row, "priority"),
			Assignee:    person{Name: t.get(row, "assignee")},
			Reporter:    person{Name: t.get(row, "reporter")},
			DueDate:     dateOnly(t.get(row, "due date", "due")),
			Created:     parseTime(t.get(row, "created")),
			Blocks:      t.all(row, "outward issue link (blocks)"),
			BlockedBy:   t.all(row, "inward issue link (blocks)"),
		}
		for _, l := range t.all(row, "labels") {
			is.Labels = append(is.Labels, strings.Fields(l)...)
		}
		if v := parseNumber(t.get(row, pointCols...)); v != nil {
			is.Estimate = v
		} else if v := parseNumber(t.get(row, "original estimate")); v != nil {
			d := *v / secondsPerDay
			is.Estimate = &d
		}
		parent := t.get(row, "parent", "parent id", "custom field (epic link)", "epic link")
		if k, ok := idToKey[parent]; ok && parent != "" {
			parent = k
		}
		is.Parent = parent
		for _, c := range t.all(row, "comment") {
			is.Comments = append(is.Comments, jiraCSVComment(c))
		}
		p.issues = append(p.issues, is)
	}
	return p, nil
}

type jiraUser struct {
	EmailAddress string `json:"emailAddress"`
	DisplayName  string `json:"displayName"`
}

func (u *jiraUser) person() person {
	if u == nil {
		return person{}
	}
	return person{Email: u.EmailAddress, Name: u.DisplayName}
}

type jiraIssue struct {
	Key    string                     `json:"key"`
	Fields map[string]json.RawMessage `json:"fields"`
}

func parseJiraJSON(content []byte) (parsed, error) {
	var doc struct {
		Issues []jiraIssue        `json:"issues"`
		Names  map[string]string `json:"names"`
	}
	if content[0] == '[' {
		if err := json.Unmarshal(content, &doc.Issues); err != nil {
			return parsed{}, badRequest("invalid Jira JSON: " + err.Error())
		}
	} else if err := json.Unmarshal(content, &doc); err != nil {
		return parsed{}, badRequest("invalid Jira JSON: " + err.Error())
	}
	if len(doc.Issues) == 0 {
		return parsed{}, badRequest(`no issues found: expected Jira's search response ({"issues": [...]})`)
	}
	// Story points live in a custom field whose id varies; "names" (expand=names) says which.
	pointFields := []string{"customfield_10016", "customfield_10026", "customfield_10002"}
	for id, name := range doc.Names {
		if n := strings.ToLower(name); strings.HasPrefix(n, "story point") {
			pointFields = append([]string{id}, pointFields...)
		}
	}
	var p parsed
	for _, ji := range doc.Issues {
		f := ji.Fields
		var named struct {
			Name           string `json:"name"`
			Key            string `json:"key"`
			StatusCategory struct {
				Key string `json:"key"`
			} `json:"statusCategory"`
		}
		get := func(k string, v any) bool { return f[k] != nil && json.Unmarshal(f[k], v) == nil }
		is := extIssue{ExtID: ji.Key, ProjectKey: keyPrefix(ji.Key)}
		get("summary", &is.Title)
		if get("issuetype", &named) {
			is.Type = named.Name
		}
		named.Name = ""
		if get("status", &named) {
			is.Status, is.Category = named.Name, jiraCategory(named.StatusCategory.Key)
		}
		named.Name = ""
		if get("priority", &named) {
			is.Priority = named.Name
		}
		var proj struct{ Key, Name string }
		if get("project", &proj) {
			is.ProjectKey, is.ProjectName = firstNonEmpty(proj.Key, is.ProjectKey), proj.Name
		}
		var u *jiraUser
		if get("assignee", &u) {
			is.Assignee = u.person()
		}
		u = nil
		if get("reporter", &u) {
			is.Reporter = u.person()
		}
		get("labels", &is.Labels)
		var s string
		if get("duedate", &s) {
			is.DueDate = dateOnly(s)
		}
		if get("created", &s) {
			is.Created = parseTime(s)
		}
		is.Description = jiraText(f["description"])
		for _, pf := range pointFields {
			var v *float64
			if get(pf, &v) && v != nil {
				is.Estimate = v
				break
			}
		}
		var secs *float64
		if is.Estimate == nil && get("timeoriginalestimate", &secs) && secs != nil {
			d := *secs / secondsPerDay
			is.Estimate = &d
		}
		var parent struct{ Key string }
		if get("parent", &parent) {
			is.Parent = parent.Key
		}
		var comments struct {
			Comments []struct {
				ID      string          `json:"id"`
				Author  *jiraUser       `json:"author"`
				Body    json.RawMessage `json:"body"`
				Created string          `json:"created"`
			} `json:"comments"`
		}
		if get("comment", &comments) {
			for _, c := range comments.Comments {
				is.Comments = append(is.Comments, extComment{ExtID: ji.Key + "#" + c.ID, Author: c.Author.person(), Body: jiraText(c.Body), Created: parseTime(c.Created)})
			}
		}
		var links []struct {
			Type struct {
				Name    string `json:"name"`
				Inward  string `json:"inward"`
				Outward string `json:"outward"`
			} `json:"type"`
			InwardIssue  *struct{ Key string } `json:"inwardIssue"`
			OutwardIssue *struct{ Key string } `json:"outwardIssue"`
		}
		if get("issuelinks", &links) {
			for _, l := range links {
				if !strings.EqualFold(l.Type.Name, "blocks") && !strings.EqualFold(l.Type.Outward, "blocks") {
					continue
				}
				if l.OutwardIssue != nil {
					is.Blocks = append(is.Blocks, l.OutwardIssue.Key)
				}
				if l.InwardIssue != nil {
					is.BlockedBy = append(is.BlockedBy, l.InwardIssue.Key)
				}
			}
		}
		p.issues = append(p.issues, is)
	}
	return p, nil
}

// jiraText flattens a description: a plain string (Jira Server, API v2) or Atlassian Document
// Format (Jira Cloud, API v3).
func jiraText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var node any
	if json.Unmarshal(raw, &node) != nil {
		return ""
	}
	var b strings.Builder
	var walk func(n any)
	walk = func(n any) {
		m, ok := n.(map[string]any)
		if !ok {
			return
		}
		switch m["type"] {
		case "text":
			t, _ := m["text"].(string)
			b.WriteString(t)
		case "hardBreak":
			b.WriteString("\n")
		case "mention":
			if a, ok := m["attrs"].(map[string]any); ok {
				t, _ := a["text"].(string)
				b.WriteString(t)
			}
		}
		kids, _ := m["content"].([]any)
		if m["type"] == "listItem" {
			b.WriteString("- ")
		}
		for _, k := range kids {
			walk(k)
		}
		switch m["type"] {
		case "paragraph", "heading", "codeBlock", "blockquote":
			b.WriteString("\n\n")
		}
	}
	walk(node)
	return strings.TrimSpace(b.String())
}

// ---- Linear ----

func linearCategory(stateType string) string {
	switch strings.ToLower(stateType) {
	case "backlog", "unstarted", "triage":
		return "todo"
	case "started":
		return "in_progress"
	case "completed", "canceled", "cancelled":
		return "done"
	}
	return ""
}

var linearPriorities = map[int]string{0: "No priority", 1: "Urgent", 2: "High", 3: "Medium", 4: "Low"}

func linearPerson(v string) person {
	if strings.Contains(v, "@") {
		return person{Email: v}
	}
	return person{Name: v}
}

func parseLinearCSV(content []byte) (parsed, error) {
	t, err := readCSV(content)
	if err != nil {
		return parsed{}, err
	}
	if !t.has("id") || !t.has("title") {
		return parsed{}, badRequest("this doesn't look like a Linear CSV export (no ID/Title columns)")
	}
	var p parsed
	if !t.has("blocked by") && !t.has("blocks") {
		p.warnings = append(p.warnings, "This export has no Blocks/Blocked by columns, so no dependencies can be imported. The JSON export (GraphQL) includes them.")
	}
	for _, row := range t.rows {
		key := t.get(row, "id")
		if key == "" {
			continue
		}
		is := extIssue{
			ExtID:       key,
			ProjectKey:  keyPrefix(key),
			ProjectName: t.get(row, "team"),
			Title:       t.get(row, "title"),
			Description: t.get(row, "description"),
			Status:      t.get(row, "status"),
			Priority:    t.get(row, "priority"),
			Assignee:    linearPerson(t.get(row, "assignee")),
			Reporter:    linearPerson(t.get(row, "creator")),
			Labels:      splitList(t.get(row, "labels")),
			DueDate:     dateOnly(t.get(row, "due date")),
			Estimate:    parseNumber(t.get(row, "estimate")),
			Parent:      t.get(row, "parent issue"),
			Created:     parseTime(t.get(row, "created")),
			Blocks:      splitList(t.get(row, "blocks")),
			BlockedBy:   splitList(t.get(row, "blocked by")),
		}
		switch {
		case t.get(row, "completed") != "" || t.get(row, "canceled") != "":
			is.Category = "done"
		case t.get(row, "started") != "":
			is.Category = "in_progress"
		}
		is.Type = linearType(is.Labels)
		p.issues = append(p.issues, is)
	}
	return p, nil
}

// Linear has no issue types: a "bug" label makes a bug; everything else is a task.
func linearType(labels []string) string {
	for _, l := range labels {
		if strings.EqualFold(l, "bug") {
			return "Bug"
		}
	}
	return "Issue"
}

type linearUser struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

func (u *linearUser) person() person {
	if u == nil {
		return person{}
	}
	return person{Email: u.Email, Name: u.Name}
}

type linearRef struct {
	Identifier string `json:"identifier"`
}

type linearIssue struct {
	Identifier    string      `json:"identifier"`
	Title         string      `json:"title"`
	Description   string      `json:"description"`
	Priority      *int        `json:"priority"`
	PriorityLabel string      `json:"priorityLabel"`
	Estimate      *float64    `json:"estimate"`
	DueDate       string      `json:"dueDate"`
	CreatedAt     string      `json:"createdAt"`
	State         *struct{ Name, Type string } `json:"state"`
	Assignee      *linearUser `json:"assignee"`
	Creator       *linearUser `json:"creator"`
	Team          *struct{ Key, Name string } `json:"team"`
	Parent        *linearRef  `json:"parent"`
	Labels        struct {
		Nodes []struct{ Name string } `json:"nodes"`
	} `json:"labels"`
	Comments struct {
		Nodes []struct {
			ID        string      `json:"id"`
			Body      string      `json:"body"`
			CreatedAt string      `json:"createdAt"`
			User      *linearUser `json:"user"`
		} `json:"nodes"`
	} `json:"comments"`
	Relations struct {
		Nodes []struct {
			Type         string     `json:"type"`
			RelatedIssue *linearRef `json:"relatedIssue"`
		} `json:"nodes"`
	} `json:"relations"`
	InverseRelations struct {
		Nodes []struct {
			Type  string     `json:"type"`
			Issue *linearRef `json:"issue"`
		} `json:"nodes"`
	} `json:"inverseRelations"`
}

func parseLinearJSON(content []byte) (parsed, error) {
	var nodes []linearIssue
	if content[0] == '[' {
		if err := json.Unmarshal(content, &nodes); err != nil {
			return parsed{}, badRequest("invalid Linear JSON: " + err.Error())
		}
	} else {
		var doc struct {
			Data struct {
				Issues struct {
					Nodes []linearIssue `json:"nodes"`
				} `json:"issues"`
			} `json:"data"`
			Issues json.RawMessage `json:"issues"`
		}
		if err := json.Unmarshal(content, &doc); err != nil {
			return parsed{}, badRequest("invalid Linear JSON: " + err.Error())
		}
		nodes = doc.Data.Issues.Nodes
		if len(nodes) == 0 && len(doc.Issues) > 0 {
			var wrapped struct {
				Nodes []linearIssue `json:"nodes"`
			}
			if json.Unmarshal(doc.Issues, &nodes) != nil && json.Unmarshal(doc.Issues, &wrapped) == nil {
				nodes = wrapped.Nodes
			}
		}
	}
	if len(nodes) == 0 {
		return parsed{}, badRequest(`no issues found: expected {"data": {"issues": {"nodes": [...]}}}`)
	}
	var p parsed
	for _, n := range nodes {
		is := extIssue{
			ExtID:       n.Identifier,
			ProjectKey:  keyPrefix(n.Identifier),
			Title:       n.Title,
			Description: n.Description,
			Estimate:    n.Estimate,
			DueDate:     dateOnly(n.DueDate),
			Created:     parseTime(n.CreatedAt),
			Assignee:    n.Assignee.person(),
			Reporter:    n.Creator.person(),
			Priority:    n.PriorityLabel,
		}
		if n.Priority != nil && is.Priority == "" {
			is.Priority = linearPriorities[*n.Priority]
		}
		if n.Team != nil {
			is.ProjectKey, is.ProjectName = firstNonEmpty(n.Team.Key, is.ProjectKey), n.Team.Name
		}
		if n.State != nil {
			is.Status, is.Category = n.State.Name, linearCategory(n.State.Type)
		}
		if n.Parent != nil {
			is.Parent = n.Parent.Identifier
		}
		for _, l := range n.Labels.Nodes {
			is.Labels = append(is.Labels, l.Name)
		}
		is.Type = linearType(is.Labels)
		for _, c := range n.Comments.Nodes {
			is.Comments = append(is.Comments, extComment{ExtID: c.ID, Author: c.User.person(), Body: c.Body, Created: parseTime(c.CreatedAt)})
		}
		for _, r := range n.Relations.Nodes {
			if r.Type == "blocks" && r.RelatedIssue != nil {
				is.Blocks = append(is.Blocks, r.RelatedIssue.Identifier)
			}
		}
		for _, r := range n.InverseRelations.Nodes {
			if r.Type == "blocks" && r.Issue != nil {
				is.BlockedBy = append(is.BlockedBy, r.Issue.Identifier)
			}
		}
		p.issues = append(p.issues, is)
	}
	return p, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
