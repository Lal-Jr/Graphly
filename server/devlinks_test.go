package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func gitlabPost(t *testing.T, url, token, event string, payload any) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("X-Gitlab-Event", event)
	req.Header.Set("X-Gitlab-Token", token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// statusOf returns the name and category of an issue's current status.
func statusOf(t *testing.T, c *apiClient, projectID, issueID string) Status {
	t.Helper()
	var snap projectSnapshot
	c.must("GET", "/api/projects/"+projectID, nil, &snap)
	for _, i := range snap.Issues {
		if i.ID == issueID {
			for _, st := range snap.Statuses {
				if st.ID == i.StatusID {
					return st
				}
			}
		}
	}
	t.Fatal("issue not found")
	return Status{}
}

func TestGitHubPushLinksCommits(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	_, ws := signup(t, c, "Dev", "push@example.com")
	var p Project
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "PU", "name": "Push"}, &p)
	var one, two, three Issue
	c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "Branch only"}, &one)
	c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "From a commit"}, &two)
	c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "Untouched"}, &three)
	var gh struct{ Secret, Path string }
	c.must("POST", "/api/projects/"+p.ID+"/github", map[string]any{}, &gh)
	hook := srv.URL + gh.Path

	// A new branch named after PU-1, with no commits yet, starts it.
	if code := signedPost(t, hook, gh.Secret, "push", map[string]any{
		"ref": "refs/heads/feature/PU-1-branch", "created": true, "commits": []any{},
		"repository": map[string]string{"full_name": "acme/app"},
	}); code != 200 {
		t.Fatalf("push: %d", code)
	}
	if st := statusOf(t, c, p.ID, one.ID); st.Category != "in_progress" {
		t.Fatalf("new branch should start PU-1, got %s", st.Name)
	}

	commit := map[string]any{
		"id": "abc1234def", "message": "Fix the parser (PU-2)\n\nlong body PU-3 is mentioned only here", "url": "https://github.com/acme/app/commit/abc1234def",
		"timestamp": "2026-10-01T10:00:00Z", "author": map[string]string{"name": "Octo Cat", "username": "octocat"},
	}
	push := map[string]any{"ref": "refs/heads/main", "commits": []any{commit}, "repository": map[string]string{"full_name": "acme/app"}}
	for i := 0; i < 2; i++ { // the redelivery must not link twice
		if code := signedPost(t, hook, gh.Secret, "push", push); code != 200 {
			t.Fatalf("push: %d", code)
		}
	}
	var commits []Commit
	c.must("GET", "/api/issues/"+two.ID+"/commits", nil, &commits)
	if len(commits) != 1 || commits[0].SHA != "abc1234def" || commits[0].Message != "Fix the parser (PU-2)" || commits[0].Author != "octocat" || commits[0].Branch != "main" {
		t.Fatalf("commits: %+v", commits)
	}
	if st := statusOf(t, c, p.ID, two.ID); st.Category != "in_progress" {
		t.Fatalf("a commit should start PU-2, got %s", st.Name)
	}
	// The body mentions PU-3 too: messages are scanned in full.
	if st := statusOf(t, c, p.ID, three.ID); st.Category != "in_progress" {
		t.Fatalf("a commit body mention should start PU-3, got %s", st.Name)
	}

	// Forward only: a commit for an issue already in review leaves it there.
	var snap projectSnapshot
	c.must("GET", "/api/projects/"+p.ID, nil, &snap)
	var review string
	for _, st := range snap.Statuses {
		if strings.Contains(st.Name, "Review") {
			review = st.ID
		}
	}
	c.must("PATCH", "/api/issues/"+one.ID, map[string]any{"statusId": review}, nil)
	commit["id"], commit["message"] = "fff0001", "More PU-1"
	signedPost(t, hook, gh.Secret, "push", push)
	if st := statusOf(t, c, p.ID, one.ID); st.ID != review {
		t.Fatalf("a commit moved PU-1 backwards to %s", st.Name)
	}

	// Tags and deleted branches are ignored.
	signedPost(t, hook, gh.Secret, "push", map[string]any{"ref": "refs/tags/v1", "commits": []any{map[string]any{"id": "t1", "message": "PU-2", "url": "https://x/t1"}}})
	c.must("GET", "/api/issues/"+two.ID+"/commits", nil, &commits)
	if len(commits) != 1 {
		t.Fatalf("tag push linked commits: %+v", commits)
	}
}

func TestGitLabWebhook(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	_, ws := signup(t, c, "Dev", "gitlab@example.com")
	var p Project
	c.must("POST", "/api/workspaces/"+ws.ID+"/projects", map[string]any{"key": "GL", "name": "Lab"}, &p)
	var blocker, waiting Issue
	c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "API"}, &blocker)
	c.must("POST", "/api/projects/"+p.ID+"/issues", map[string]any{"title": "UI"}, &waiting)
	c.must("POST", "/api/issues/"+waiting.ID+"/blockers", map[string]string{"blockerId": blocker.ID}, nil)

	// Not connected yet: the hook doesn't exist.
	if code, _ := gitlabPost(t, srv.URL+"/api/hooks/gitlab/"+p.ID, "x", "Merge Request Hook", map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("disabled hook: %d", code)
	}
	var gl struct {
		Enabled      bool
		Secret, Path string
	}
	c.must("POST", "/api/projects/"+p.ID+"/gitlab", map[string]any{}, &gl)
	hook := srv.URL + gl.Path
	mr := func(state, action string, draft bool) map[string]any {
		return map[string]any{
			"object_kind": "merge_request",
			"user":        map[string]string{"username": "tanuki"},
			"project":     map[string]string{"path_with_namespace": "acme/app"},
			"object_attributes": map[string]any{
				"iid": 3, "title": "Login API", "description": "", "url": "https://gitlab.com/acme/app/-/merge_requests/3",
				"state": state, "action": action, "source_branch": "GL-1-login", "draft": draft,
			},
		}
	}
	if code, _ := gitlabPost(t, hook, "wrong", "Merge Request Hook", mr("opened", "open", false)); code != http.StatusUnauthorized {
		t.Fatalf("bad token accepted: %d", code)
	}

	gitlabPost(t, hook, gl.Secret, "Merge Request Hook", mr("opened", "open", true))
	if st := statusOf(t, c, p.ID, blocker.ID); st.Category != "in_progress" || strings.Contains(st.Name, "Review") {
		t.Fatalf("draft MR should start the issue, got %s", st.Name)
	}
	gitlabPost(t, hook, gl.Secret, "Merge Request Hook", mr("opened", "update", false))
	gitlabPost(t, hook, gl.Secret, "Merge Request Hook", mr("opened", "open", false))
	if st := statusOf(t, c, p.ID, blocker.ID); st.Name != "In Review" {
		t.Fatalf("opened MR should move to review, got %s", st.Name)
	}
	var prs []PullRequest
	c.must("GET", "/api/issues/"+blocker.ID+"/prs", nil, &prs)
	if len(prs) != 1 || prs[0].Provider != "gitlab" || prs[0].Number != 3 || prs[0].State != "open" || prs[0].Author != "tanuki" {
		t.Fatalf("linked MRs: %+v", prs)
	}

	gitlabPost(t, hook, gl.Secret, "Merge Request Hook", mr("merged", "merge", false))
	if st := statusOf(t, c, p.ID, blocker.ID); st.Category != "done" {
		t.Fatalf("merged MR should move to done, got %s", st.Name)
	}
	var acts []Activity
	c.must("GET", "/api/issues/"+waiting.ID+"/activity", nil, &acts)
	if len(acts) == 0 || acts[0].Kind != "unblocked" {
		t.Fatalf("merge should unblock the dependent, got %+v", acts)
	}
	gitlabPost(t, hook, gl.Secret, "Merge Request Hook", mr("opened", "reopen", false))
	if st := statusOf(t, c, p.ID, blocker.ID); st.Category != "done" {
		t.Fatalf("issue moved backwards to %s", st.Name)
	}

	// Push hook: links commits by message.
	code, res := gitlabPost(t, hook, gl.Secret, "Push Hook", map[string]any{
		"ref": "refs/heads/GL-2-ui", "before": strings.Repeat("0", 40), "after": "123",
		"project": map[string]string{"path_with_namespace": "acme/app"},
		"commits": []any{map[string]any{"id": "c0ffee", "message": "Start the UI", "url": "https://gitlab.com/acme/app/-/commit/c0ffee",
			"timestamp": "2026-10-02T09:00:00+02:00", "author": map[string]string{"name": "Tanuki"}}},
	})
	if code != 200 || res["linked"] != float64(1) {
		t.Fatalf("push hook: %d %v", code, res)
	}
	var commits []Commit
	c.must("GET", "/api/issues/"+waiting.ID+"/commits", nil, &commits)
	if len(commits) != 1 || commits[0].Provider != "gitlab" || commits[0].Branch != "GL-2-ui" {
		t.Fatalf("commits: %+v", commits)
	}

	// Disabling removes the hook; the audit log records both changes.
	c.must("DELETE", "/api/projects/"+p.ID+"/gitlab", nil, nil)
	if code, _ := gitlabPost(t, hook, gl.Secret, "Merge Request Hook", mr("opened", "open", false)); code != http.StatusNotFound {
		t.Fatalf("hook still active after disable: %d", code)
	}
	var audit []map[string]any
	c.must("GET", "/api/workspaces/"+ws.ID+"/audit", nil, &audit)
	var actions []string
	for _, a := range audit {
		actions = append(actions, a["action"].(string))
	}
	if j := strings.Join(actions, ","); !strings.Contains(j, "webhook.gitlab.rotate") || !strings.Contains(j, "webhook.gitlab.disable") {
		t.Fatalf("audit: %v", actions)
	}
}
