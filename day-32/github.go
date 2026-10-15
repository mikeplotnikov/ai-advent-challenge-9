package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// botLogin is who GITHUB_TOKEN comments as; only its comments are ever edited.
const botLogin = "github-actions[bot]"

type github struct {
	API   string // https://api.github.com, or GITHUB_API_URL on the runner
	Token string
	Repo  string // owner/name
	HTTP  *http.Client
}

type issueComment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
}

// upsert edits this reviewer's earlier comment on the PR or posts a new one, and returns
// the comment's URL as GitHub reports it.
func (g github) upsert(ctx context.Context, pr int, body string) (string, error) {
	existing, err := g.findOwn(ctx, pr)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return "", err
	}
	method, url := http.MethodPost, fmt.Sprintf("%s/repos/%s/issues/%d/comments", g.API, g.Repo, pr)
	if existing != 0 {
		method, url = http.MethodPatch, fmt.Sprintf("%s/repos/%s/issues/comments/%d", g.API, g.Repo, existing)
	}
	raw, err := g.do(ctx, method, url, payload)
	if err != nil {
		return "", err
	}
	var saved issueComment
	if err := json.Unmarshal(raw, &saved); err != nil {
		return "", fmt.Errorf("GitHub ответил не JSON на сохранение комментария: %w", err)
	}
	// The status code alone does not prove the comment exists: the body must name it.
	if saved.ID == 0 || saved.HTMLURL == "" || !strings.Contains(saved.Body, marker) {
		return "", fmt.Errorf("GitHub не подтвердил комментарий: id=%d url=%q", saved.ID, saved.HTMLURL)
	}
	return saved.HTMLURL, nil
}

func (g github) findOwn(ctx context.Context, pr int) (int64, error) {
	for page := 1; page <= 20; page++ {
		raw, err := g.do(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/issues/%d/comments?per_page=100&page=%d", g.API, g.Repo, pr, page), nil)
		if err != nil {
			return 0, err
		}
		var comments []issueComment
		if err := json.Unmarshal(raw, &comments); err != nil {
			return 0, fmt.Errorf("GitHub ответил не списком комментариев: %w", err)
		}
		for _, c := range comments {
			if c.User.Login == botLogin && strings.HasPrefix(c.Body, marker) {
				return c.ID, nil
			}
		}
		if len(comments) < 100 {
			return 0, nil
		}
	}
	return 0, nil
}

func (g github) do(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := g.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("GitHub %s %s: %s: %s", method, url, resp.Status, strings.TrimSpace(string(raw[:min(len(raw), 500)])))
	}
	return raw, nil
}
