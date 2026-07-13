package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type GitHubClient struct {
	BaseURL string
	Client  *http.Client
}

func NewGitHubClient() *GitHubClient {
	return &GitHubClient{BaseURL: "https://api.github.com", Client: &http.Client{Timeout: 20 * time.Second}}
}

func (g *GitHubClient) ListRepositories(ctx context.Context, token, owner string) ([]GitHubRepository, error) {
	if token == "" || owner == "" {
		return nil, errors.New("用户名、resource owner 和 PAT 均不能为空")
	}
	var result []GitHubRepository
	endpoint := g.BaseURL + "/user/repos?per_page=100&sort=full_name&affiliation=owner,collaborator,organization_member"
	for page := 0; endpoint != "" && page < 20; page++ {
		var payload []struct {
			ID            int64  `json:"id"`
			FullName      string `json:"full_name"`
			Name          string `json:"name"`
			Private       bool   `json:"private"`
			Fork          bool   `json:"fork"`
			Archived      bool   `json:"archived"`
			DefaultBranch string `json:"default_branch"`
			HTMLURL       string `json:"html_url"`
			SSHURL        string `json:"ssh_url"`
			Owner         struct {
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"owner"`
			Permissions struct {
				Admin bool `json:"admin"`
			} `json:"permissions"`
		}
		next, err := g.doJSON(ctx, http.MethodGet, endpoint, token, nil, &payload)
		if err != nil {
			return nil, err
		}
		for _, item := range payload {
			if !strings.EqualFold(item.Owner.Login, owner) {
				continue
			}
			repo := GitHubRepository{ID: item.ID, FullName: item.FullName, Owner: item.Owner.Login, Name: item.Name, Private: item.Private, Fork: item.Fork, Archived: item.Archived, DefaultBranch: item.DefaultBranch, WebURL: item.HTMLURL, SSHURL: item.SSHURL, Admin: item.Permissions.Admin, OwnerType: item.Owner.Type}
			switch {
			case !repo.Private:
				repo.DisabledWhy = "公开仓库不可启用"
			case repo.Fork:
				repo.DisabledWhy = "Fork 仓库不可启用"
			case repo.Archived:
				repo.DisabledWhy = "归档仓库不可启用"
			case !repo.Admin:
				repo.DisabledWhy = "缺少仓库管理权限"
			default:
				repo.Eligible = true
			}
			result = append(result, repo)
		}
		endpoint = next
	}
	return result, nil
}

func (g *GitHubClient) CreateDeployKey(ctx context.Context, token string, repo GitHubRepository, title, publicKey string) (int64, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/keys", g.BaseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	var existing []struct {
		ID       int64  `json:"id"`
		Title    string `json:"title"`
		Key      string `json:"key"`
		ReadOnly bool   `json:"read_only"`
	}
	if _, err := g.doJSON(ctx, http.MethodGet, endpoint+"?per_page=100", token, nil, &existing); err != nil {
		return 0, err
	}
	for _, key := range existing {
		if key.Title == title && sshPublicMaterial(key.Key) == sshPublicMaterial(publicKey) {
			if key.ReadOnly {
				return 0, errors.New("同名 Deploy Key 已存在但没有写权限，请先在 GitHub 删除")
			}
			return key.ID, nil
		}
	}
	body := map[string]any{"title": title, "key": strings.TrimSpace(publicKey), "read_only": false}
	var response struct {
		ID       int64 `json:"id"`
		ReadOnly bool  `json:"read_only"`
	}
	_, err := g.doJSON(ctx, http.MethodPost, endpoint, token, body, &response)
	if err != nil {
		return 0, err
	}
	if response.ID == 0 || response.ReadOnly {
		return 0, errors.New("GitHub 未创建可写 Deploy Key")
	}
	return response.ID, nil
}

func sshPublicMaterial(value string) string {
	fields := strings.Fields(value)
	if len(fields) < 2 {
		return strings.TrimSpace(value)
	}
	return fields[0] + " " + fields[1]
}

func (g *GitHubClient) RepositoryPrivate(ctx context.Context, owner, name string) (bool, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s", g.BaseURL, url.PathEscape(owner), url.PathEscape(name))
	var response struct {
		Private bool `json:"private"`
	}
	_, err := g.doJSON(ctx, http.MethodGet, endpoint, "", nil, &response)
	if err == nil {
		return response.Private, nil
	}
	var apiErr *GitHubAPIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return true, nil // 匿名 404 只表示“未发现公开仓库”，调用方还必须完成 SSH 验证。
	}
	return false, err
}

type GitHubAPIError struct {
	Status  int
	Message string
}

func (e *GitHubAPIError) Error() string { return fmt.Sprintf("GitHub API %d: %s", e.Status, e.Message) }

func (g *GitHubClient) doJSON(ctx context.Context, method, endpoint, token string, body any, output any) (string, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "github-notes-archiver")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := g.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var api struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &api)
		if api.Message == "" {
			api.Message = http.StatusText(resp.StatusCode)
		}
		return "", &GitHubAPIError{Status: resp.StatusCode, Message: api.Message}
	}
	if output != nil && len(data) > 0 {
		if err := json.Unmarshal(data, output); err != nil {
			return "", err
		}
	}
	return nextLink(resp.Header.Get("Link")), nil
}

func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		pieces := strings.Split(strings.TrimSpace(part), ";")
		if len(pieces) != 2 || !strings.Contains(pieces[1], `rel="next"`) {
			continue
		}
		return strings.Trim(strings.TrimSpace(pieces[0]), "<>")
	}
	return ""
}

func repositoryID(n int64) string { return strconv.FormatInt(n, 10) }
