package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func fixtureText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/import/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseExports(t *testing.T) {
	p, format, err := parseExport("jira", []byte(fixtureText(t, "jira.csv")))
	if err != nil || format != "csv" || len(p.issues) != 6 {
		t.Fatalf("jira csv: %v %s %d", err, format, len(p.issues))
	}
	eng2 := p.issues[1]
	if eng2.Parent != "ENG-1" || *eng2.Estimate != 5 || len(eng2.Labels) != 2 || len(eng2.Blocks) != 2 || eng2.DueDate != "2025-10-20" {
		t.Fatalf("ENG-2: %+v", eng2)
	}
	if c := eng2.Comments[0]; c.Body != "Looks good" || !c.Author.empty() || c.Created == nil {
		t.Fatalf("comment: %+v", c)
	}
	if eng3 := p.issues[2]; *eng3.Estimate != 2 || eng3.DueDate != "2025-10-15" || eng3.Assignee.Name != "carol@example.com" {
		t.Fatalf("ENG-3: %+v", eng3)
	}

	p, format, err = parseExport("jira", []byte(fixtureText(t, "jira.json")))
	if err != nil || format != "json" || len(p.issues) != 2 {
		t.Fatalf("jira json: %v", err)
	}
	if w := p.issues[0]; w.Description != "Hero and pricing." || *w.Estimate != 3 || w.Assignee.Email != "BOB@example.com" || len(w.Blocks) != 1 {
		t.Fatalf("WEB-10: %+v", w)
	}
	if w := p.issues[1]; *w.Estimate != 0.5 || len(w.BlockedBy) != 1 || len(w.Blocks) != 0 {
		t.Fatalf("WEB-11 (relates links must be ignored): %+v", w)
	}

	p, _, err = parseExport("linear", []byte(fixtureText(t, "linear.csv")))
	if err != nil || len(p.issues) != 3 || p.issues[0].Category != "in_progress" || p.issues[2].Category != "done" || p.issues[1].Type != "Bug" {
		t.Fatalf("linear csv: %v %+v", err, p.issues)
	}
	p, _, err = parseExport("linear", []byte(fixtureText(t, "linear.json")))
	if err != nil || len(p.issues) != 2 || p.issues[0].Priority != "High" || p.issues[1].Priority != "Urgent" || len(p.issues[0].Blocks) != 1 {
		t.Fatalf("linear json: %v %+v", err, p.issues)
	}
	if _, _, err := parseExport("linear", []byte("a,b\n1,2\n")); err == nil {
		t.Fatal("accepted a CSV that isn't a Linear export")
	}
}

func TestImport(t *testing.T) {
	srv, app := newTestApp(t, nil)
	alice := newClient(t, srv)
	_, ws := signup(t, alice, "Alice Admin", "alice@example.com")
	for _, m := range []struct{ name, email string }{{"Bob Builder", "bob@example.com"}, {"Carol", "carol@example.com"}} {
		c := newClient(t, srv)
		signup(t, c, m.name, m.email)
		var inv struct{ Token string }
		alice.must("POST", "/api/workspaces/"+ws.ID+"/invites", map[string]any{}, &inv)
		c.must("POST", "/api/invites/"+inv.Token+"/accept", map[string]any{}, nil)
	}
	url := "/api/workspaces/" + ws.ID + "/import"
	body := map[string]any{"source": "jira", "content": fixtureText(t, "jira.csv"), "dryRun": true}

	// Dry run: the full report, nothing written.
	var rep importReport
	alice.must("POST", url, body, &rep)
	if !rep.DryRun || rep.Issues.Created != 6 || len(rep.Projects) != 1 || rep.Projects[0].Action != "create" || rep.Projects[0].Key != "ENG" {
		t.Fatalf("dry run: %+v", rep)
	}
	if rep.Links.Total != 5 || rep.Links.Created != 3 || rep.Links.DroppedCount != 2 {
		t.Fatalf("links: %+v", rep.Links)
	}
	reasons := map[string]string{}
	for _, d := range rep.Links.Dropped {
		reasons[d.Blocker+">"+d.Blocked] = d.Reason
	}
	if !strings.Contains(reasons["ENG-5>ENG-2"], "cycle") || !strings.Contains(reasons["ENG-5>OPS-9"], "isn't in this import") {
		t.Fatalf("dropped: %+v", rep.Links.Dropped)
	}
	if rep.Unmapped.People["Zed Unknown"] != 1 || len(rep.StatusesCreated) != 2 || rep.Comments.Created != 1 {
		t.Fatalf("unmapped/statuses/comments: %+v %+v %+v", rep.Unmapped, rep.StatusesCreated, rep.Comments)
	}
	var projects []projectSummary
	alice.must("GET", "/api/workspaces/"+ws.ID+"/projects", nil, &projects)
	if len(projects) != 0 {
		t.Fatalf("dry run wrote %d projects", len(projects))
	}

	// The real run matches the preview.
	body["dryRun"] = false
	var real importReport
	alice.must("POST", url, body, &real)
	real.DryRun = true
	real.Projects[0].ID = rep.Projects[0].ID
	if !jsonEqual(real, rep) {
		t.Fatalf("real run differs from preview:\n%+v\n%+v", real, rep)
	}
	alice.must("GET", "/api/workspaces/"+ws.ID+"/projects", nil, &projects)
	var snap projectSnapshot
	alice.must("GET", "/api/projects/"+projects[0].ID, nil, &snap)
	byNum := map[int]Issue{}
	for _, i := range snap.Issues {
		byNum[i.Number] = i
	}
	epic, pay, ui, bug, odd := byNum[1], byNum[2], byNum[3], byNum[4], byNum[7]
	if epic.Type != "epic" || pay.EpicID == nil || *pay.EpicID != epic.ID || ui.EpicID == nil || odd.Title != "Odd one" {
		t.Fatalf("numbers/epics: %+v", byNum)
	}
	if pay.AssigneeID == nil || pay.Priority != "high" || pay.Estimate != 5 || ui.Priority != "highest" || ui.Estimate != 2 || bug.Priority != "low" {
		t.Fatalf("fields: %+v %+v %+v", pay, ui, bug)
	}
	if byNum[5].AssigneeID != nil || byNum[5].Estimate != 1 || odd.Estimate != 0 {
		t.Fatalf("unknown assignee / missing estimate: %+v %+v", byNum[5], odd)
	}
	if len(snap.Links) != 3 {
		t.Fatalf("links: %+v", snap.Links)
	}
	var issue Issue
	alice.must("POST", "/api/projects/"+projects[0].ID+"/issues", map[string]any{"title": "next"}, &issue)
	if issue.Number != 8 {
		t.Fatalf("next number after import: %d", issue.Number)
	}

	// Re-running changes nothing; editing the export updates in place.
	var again importReport
	alice.must("POST", url, body, &again)
	if again.Issues.Created != 0 || again.Issues.Unchanged != 6 || again.Links.Created != 0 || again.Links.Existing != 3 || again.Comments.Existing != 1 || again.Projects[0].Action != "existing" {
		t.Fatalf("re-run: %+v", again)
	}
	body["content"] = strings.Replace(fixtureText(t, "jira.csv"), "Payments API", "Payments API v2", 1)
	alice.must("POST", url, body, &again)
	if again.Issues.Updated != 1 || again.Issues.Created != 0 {
		t.Fatalf("edited re-run: %+v", again.Issues)
	}
	var count int
	app.db.QueryRow(context.Background(), `SELECT count(*) FROM issues`).Scan(&count)
	if count != 7 {
		t.Fatalf("%d issues after re-runs", count)
	}

	// Linear: cycle dropped, sub-issue parent reported, canceled → a done status.
	var lin importReport
	alice.must("POST", url, map[string]any{"source": "linear", "content": fixtureText(t, "linear.csv")}, &lin)
	if lin.Issues.Created != 3 || lin.Links.Created != 2 || lin.Links.DroppedCount != 1 || lin.Unmapped.Parents != 1 {
		t.Fatalf("linear: %+v", lin)
	}
	var linJSON importReport
	alice.must("POST", url, map[string]any{"source": "linear", "content": fixtureText(t, "linear.json")}, &linJSON)
	if linJSON.Issues.Created != 2 || linJSON.Links.Created != 1 || linJSON.Unmapped.People["Xavier"] != 1 {
		t.Fatalf("linear json: %+v", linJSON)
	}

	// Members can't import; bad input is a 400.
	bob := newClient(t, srv)
	signup(t, bob, "Mallory", "mallory@example.com")
	if code := bob.do("POST", url, body, nil); code != 403 && code != 404 {
		t.Fatalf("non-member import: %d", code)
	}
	if code := alice.do("POST", url, map[string]any{"source": "asana", "content": "x"}, nil); code != 400 {
		t.Fatalf("unknown source: %d", code)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
