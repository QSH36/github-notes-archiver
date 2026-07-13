package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/example/github-notes-archiver/internal/webui"
)

type Server struct {
	store        *Store
	git          *GitManager
	github       *GitHubClient
	version      string
	trustedHosts map[string]struct{}
	mu           sync.Mutex
	sessions     map[string]*adminSession
	discoveries  map[string]*DiscoverySession
	previews     map[string]*ImportPreview
	syncing      bool
}

func NewServer(store *Store, version string) *Server {
	return &Server{store: store, git: NewGitManager(store), github: NewGitHubClient(), version: version, trustedHosts: parseTrustedHosts(os.Getenv("GNA_TRUSTED_HOSTS")), sessions: map[string]*adminSession{}, discoveries: map[string]*DiscoverySession{}, previews: map[string]*ImportPreview{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/api/v1/session", s.session)
	mux.HandleFunc("/api/v1/status", s.auth(s.status))
	mux.HandleFunc("/api/v1/config", s.auth(s.config))
	mux.HandleFunc("/api/v1/github/discovery-sessions", s.auth(s.discovery))
	mux.HandleFunc("/api/v1/github/discovery-sessions/", s.auth(s.discoveryRepositories))
	mux.HandleFunc("/api/v1/repositories/activations", s.auth(s.activateRepositories))
	mux.HandleFunc("/api/v1/repositories/manual", s.auth(s.manualRepository))
	mux.HandleFunc("/api/v1/repositories", s.auth(s.repositories))
	mux.HandleFunc("/api/v1/repositories/", s.auth(s.repositoryAction))
	mux.HandleFunc("/api/v1/notes/versions", s.auth(s.noteVersion))
	mux.HandleFunc("/api/v1/queue", s.auth(s.queue))
	mux.HandleFunc("/api/v1/sync", s.auth(s.syncNow))
	mux.HandleFunc("/api/v1/imports/previews", s.auth(s.importPreview))
	mux.HandleFunc("/api/v1/imports/runs", s.auth(s.importRun))
	mux.HandleFunc("/api/v1/events", s.auth(s.events))
	mux.Handle("/", webui.Handler())
	return s.securityHeaders(mux)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": s.version, "time": time.Now()})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var input struct {
			Token string `json:"token"`
		}
		if err := decodeJSON(w, r, &input, 8<<10); err != nil {
			return
		}
		if !s.store.verifyToken(input.Token) {
			time.Sleep(300 * time.Millisecond)
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "管理令牌无效")
			return
		}
		_ = os.Remove(filepath.Join(s.store.dataDir, "initial-admin-token"))
		id, _ := randomToken(32)
		csrf, _ := randomToken(24)
		now := time.Now()
		s.mu.Lock()
		s.sessions[id] = &adminSession{ID: id, CSRF: csrf, CreatedAt: now, LastSeen: now}
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "gna_session", Value: id, Path: "/", HttpOnly: true, Secure: s.isTrustedHost(requestHost(r.Host)), SameSite: http.SameSiteStrictMode, MaxAge: 8 * 3600})
		writeJSON(w, http.StatusCreated, map[string]string{"csrf_token": csrf})
	case http.MethodDelete:
		cookie, _ := r.Cookie("gna_session")
		if cookie != nil {
			s.mu.Lock()
			session, ok := s.sessions[cookie.Value]
			if !ok || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRF)) != 1 || !s.validOrigin(r) {
				s.mu.Unlock()
				writeError(w, http.StatusForbidden, "csrf_rejected", "请求来源或 CSRF token 无效")
				return
			}
			delete(s.sessions, cookie.Value)
			for id, discovery := range s.discoveries {
				if discovery.AdminSession == cookie.Value {
					discovery.Token = ""
					delete(s.discoveries, id)
				}
			}
			s.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: "gna_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.isTrustedHost(requestHost(r.Host)), SameSite: http.SameSiteStrictMode})
		w.WriteHeader(http.StatusNoContent)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST 或 DELETE")
	}
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	repos := s.store.repositories()
	queued := 0
	for _, repo := range repos {
		items, _ := s.store.queue(repo.ID)
		queued += len(items)
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": s.version, "repositories": len(repos), "enabled": countEnabled(repos), "queued": queued, "timezone": s.store.Config().Timezone})
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.store.Config())
	case http.MethodPut:
		var input Config
		if err := decodeJSON(w, r, &input, 32<<10); err != nil {
			return
		}
		if err := s.store.updateConfig(input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, s.store.Config())
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET 或 PUT")
	}
}

func (s *Server) discovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST")
		return
	}
	var input struct {
		Username string `json:"username"`
		Owner    string `json:"owner"`
		Token    string `json:"token"`
	}
	if err := decodeJSON(w, r, &input, 64<<10); err != nil {
		return
	}
	repositories, err := s.github.ListRepositories(r.Context(), strings.TrimSpace(input.Token), strings.TrimSpace(input.Owner))
	if err != nil {
		writeError(w, http.StatusBadGateway, "github_error", err.Error())
		return
	}
	id, _ := randomToken(24)
	sessionID := sessionIDFrom(r.Context())
	discovery := &DiscoverySession{ID: id, AdminSession: sessionID, Username: strings.TrimSpace(input.Username), Owner: strings.TrimSpace(input.Owner), Token: strings.TrimSpace(input.Token), Repositories: repositories, ExpiresAt: time.Now().Add(10 * time.Minute)}
	s.mu.Lock()
	s.discoveries[id] = discovery
	s.mu.Unlock()
	time.AfterFunc(10*time.Minute, func() {
		s.mu.Lock()
		if current, ok := s.discoveries[id]; ok && current == discovery {
			current.Token = ""
			delete(s.discoveries, id)
		}
		s.mu.Unlock()
	})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "owner": discovery.Owner, "expires_at": discovery.ExpiresAt, "repositories": repositories})
}

func (s *Server) discoveryRepositories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/github/discovery-sessions/"), "/")
	if len(parts) != 2 || parts[1] != "repositories" {
		http.NotFound(w, r)
		return
	}
	discovery, ok := s.getDiscovery(parts[0], sessionIDFrom(r.Context()))
	if !ok {
		writeError(w, http.StatusNotFound, "discovery_expired", "临时授权不存在或已过期")
		return
	}
	writeJSON(w, http.StatusOK, discovery.Repositories)
}

func (s *Server) activateRepositories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST")
		return
	}
	var input struct {
		DiscoveryID   string  `json:"discovery_id"`
		RepositoryIDs []int64 `json:"repository_ids"`
		Acknowledge   bool    `json:"acknowledge_pat_lifecycle"`
	}
	if err := decodeJSON(w, r, &input, 64<<10); err != nil {
		return
	}
	if !input.Acknowledge {
		writeError(w, http.StatusBadRequest, "acknowledgement_required", "必须确认删除 PAT 会连带删除 Deploy Key")
		return
	}
	discovery, ok := s.getDiscovery(input.DiscoveryID, sessionIDFrom(r.Context()))
	if !ok {
		writeError(w, http.StatusNotFound, "discovery_expired", "临时授权不存在或已过期")
		return
	}
	selected := map[int64]bool{}
	for _, id := range input.RepositoryIDs {
		selected[id] = true
	}
	type result struct {
		Repository string `json:"repository"`
		Success    bool   `json:"success"`
		Error      string `json:"error,omitempty"`
	}
	var results []result
	for _, candidate := range discovery.Repositories {
		if !selected[candidate.ID] {
			continue
		}
		item := result{Repository: candidate.FullName}
		if !candidate.Eligible {
			item.Error = candidate.DisabledWhy
			results = append(results, item)
			continue
		}
		repo, err := s.activateOne(r.Context(), discovery.Token, candidate)
		if err != nil {
			item.Error = err.Error()
			if repo.ID != "" {
				_ = s.store.upsertRepository(repo)
			}
			s.store.log("error", "repository_activation_failed", repositoryID(candidate.ID), err.Error())
		} else {
			item.Success = true
			_ = s.store.upsertRepository(repo)
			s.store.log("info", "repository_activated", repo.ID, repo.FullName)
		}
		results = append(results, item)
	}
	s.mu.Lock()
	discovery.Token = ""
	delete(s.discoveries, input.DiscoveryID)
	s.mu.Unlock()
	writeJSON(w, http.StatusMultiStatus, results)
}

func (s *Server) activateOne(ctx context.Context, token string, candidate GitHubRepository) (RepositoryConfig, error) {
	id := repositoryID(candidate.ID)
	if existing, ok := s.store.repository(id); ok && existing.FullName == candidate.FullName && existing.RemoteKeyID != 0 {
		if err := s.git.TestAndClone(ctx, existing); err != nil {
			existing.Enabled, existing.Health, existing.Error = false, "error", err.Error()
			return existing, err
		}
		existing.Enabled, existing.Health, existing.Error = true, "healthy", ""
		return existing, nil
	}
	keyPath, publicKey, err := s.git.GenerateDeployKey(ctx, id)
	if err != nil {
		return RepositoryConfig{}, err
	}
	remoteID, err := s.github.CreateDeployKey(ctx, token, candidate, "github-notes-archiver "+id, publicKey)
	if err != nil {
		return RepositoryConfig{}, err
	}
	repo := RepositoryConfig{ID: id, FullName: candidate.FullName, Owner: candidate.Owner, DefaultBranch: candidate.DefaultBranch, ArchivePath: defaultArchivePath, AuthMode: "deploy_key", KeyPath: keyPath, RemoteKeyID: remoteID, Enabled: true, Health: "checking", CloneURL: candidate.SSHURL, WebURL: candidate.WebURL}
	if err := s.store.upsertRepository(repo); err != nil {
		return repo, err
	}
	if err := s.git.TestAndClone(ctx, repo); err != nil {
		repo.Enabled, repo.Health, repo.Error = false, "error", err.Error()
		return repo, err
	}
	repo.Health = "healthy"
	return repo, nil
}

func (s *Server) manualRepository(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 POST")
		return
	}
	var input struct {
		FullName      string `json:"full_name"`
		CloneURL      string `json:"clone_url"`
		DefaultBranch string `json:"default_branch"`
		PrivateKey    string `json:"private_key"`
		AuthorName    string `json:"author_name"`
		AuthorEmail   string `json:"author_email"`
	}
	if err := decodeJSON(w, r, &input, 96<<10); err != nil {
		return
	}
	if !fullNamePattern.MatchString(input.FullName) || !safeBranch(input.DefaultBranch) || !strings.EqualFold(input.CloneURL, "git@github.com:"+input.FullName+".git") {
		writeError(w, http.StatusBadRequest, "invalid_repository", "仓库或默认分支无效")
		return
	}
	if err := s.store.updateConfig(Config{AuthorName: input.AuthorName, AuthorEmail: input.AuthorEmail}); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_identity", err.Error())
		return
	}
	id := hashToken(strings.ToLower(input.FullName))[:16]
	keyPath, err := s.git.SavePrivateKey(r.Context(), id, input.PrivateKey)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_private_key", err.Error())
		return
	}
	parts := strings.Split(input.FullName, "/")
	repo := RepositoryConfig{ID: id, FullName: input.FullName, Owner: parts[0], DefaultBranch: input.DefaultBranch, ArchivePath: defaultArchivePath, AuthMode: "uploaded_key", KeyPath: keyPath, Enabled: true, Health: "checking", CloneURL: input.CloneURL, WebURL: "https://github.com/" + input.FullName}
	private, err := s.github.RepositoryPrivate(r.Context(), repo.Owner, strings.TrimPrefix(repo.FullName, repo.Owner+"/"))
	if err != nil || !private {
		_ = os.Remove(keyPath)
		if err != nil {
			writeError(w, http.StatusBadGateway, "privacy_check_failed", err.Error())
		} else {
			writeError(w, http.StatusBadRequest, "private_repository_required", "仅允许私有仓库")
		}
		return
	}
	if err := s.git.TestAndClone(r.Context(), repo); err != nil {
		writeError(w, http.StatusBadGateway, "repository_test_failed", err.Error())
		return
	}
	repo.Health = "healthy"
	if err := s.store.upsertRepository(repo); err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, repo)
}

func (s *Server) repositories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "仅支持 GET")
		return
	}
	writeJSON(w, http.StatusOK, s.store.repositories())
}

func (s *Server) repositoryAction(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/repositories/")
	parts := strings.Split(trimmed, "/")
	if len(parts) < 1 {
		http.NotFound(w, r)
		return
	}
	repo, ok := s.store.repository(parts[0])
	if !ok {
		writeError(w, http.StatusNotFound, "repository_not_found", "仓库不存在")
		return
	}
	if len(parts) == 1 && r.Method == http.MethodPut {
		var input struct {
			Enabled     *bool  `json:"enabled"`
			ArchivePath string `json:"archive_path"`
		}
		if err := decodeJSON(w, r, &input, 16<<10); err != nil {
			return
		}
		if input.Enabled != nil {
			repo.Enabled = *input.Enabled
		}
		if input.ArchivePath != "" {
			clean, err := safeLogicalPath(input.ArchivePath)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_archive_path", err.Error())
				return
			}
			repo.ArchivePath = path.Clean(strings.ReplaceAll(clean, "\\", "/"))
		}
		if err := s.store.upsertRepository(repo); err != nil {
			writeError(w, 500, "store_error", err.Error())
			return
		}
		writeJSON(w, 200, repo)
		return
	}
	if len(parts) == 2 && parts[1] == "test" && r.Method == http.MethodPost {
		err := s.git.TestAndClone(r.Context(), repo)
		if err != nil {
			repo.Health, repo.Error = "error", err.Error()
		} else {
			repo.Health, repo.Error = "healthy", ""
		}
		_ = s.store.upsertRepository(repo)
		if err != nil {
			writeError(w, 502, "repository_test_failed", err.Error())
			return
		}
		writeJSON(w, 200, repo)
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持此操作")
}

func (s *Server) noteVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method_not_allowed", "仅支持 POST")
		return
	}
	var input struct {
		RepositoryID string `json:"repository_id"`
		LogicalPath  string `json:"logical_path"`
		Title        string `json:"title"`
		Content      string `json:"content"`
		VersionAt    string `json:"version_at"`
	}
	if err := decodeJSON(w, r, &input, 2<<20); err != nil {
		return
	}
	repo, ok := s.store.repository(input.RepositoryID)
	if !ok || !repo.Enabled {
		writeError(w, 400, "repository_required", "必须选择一个已启用仓库")
		return
	}
	if len(input.Content) == 0 || len(input.Content) > 1<<20 || strings.IndexByte(input.Content, 0) >= 0 {
		writeError(w, 400, "invalid_content", "内容必须是 1 MiB 内的纯文本")
		return
	}
	if _, err := safeLogicalPath(input.LogicalPath); err != nil {
		writeError(w, 400, "invalid_path", err.Error())
		return
	}
	versionAt := time.Now()
	if input.VersionAt != "" {
		parsed, err := time.Parse(time.RFC3339, input.VersionAt)
		if err != nil {
			writeError(w, 400, "invalid_time", "version_at 必须为 RFC 3339")
			return
		}
		if parsed.After(time.Now().Add(5 * time.Minute)) {
			writeError(w, 400, "future_time", "版本时间不能位于未来")
			return
		}
		versionAt = parsed
	}
	id, _ := randomToken(18)
	event := ArchiveEvent{ID: id, RepositoryID: repo.ID, LogicalPath: input.LogicalPath, Title: input.Title, ContentSHA256: contentHash(input.Content), VersionAt: versionAt, Source: "gui", Status: "queued", Content: input.Content}
	if err := s.store.saveEvent(event); err != nil {
		writeError(w, 500, "store_error", err.Error())
		return
	}
	event.Content = ""
	writeJSON(w, 201, event)
}

func (s *Server) queue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method_not_allowed", "仅支持 GET")
		return
	}
	var all []ArchiveEvent
	for _, repo := range s.store.repositories() {
		items, _ := s.store.queue(repo.ID)
		for i := range items {
			items[i].Content = ""
		}
		all = append(all, items...)
	}
	writeJSON(w, 200, all)
}

func (s *Server) syncNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method_not_allowed", "仅支持 POST")
		return
	}
	var input struct {
		RepositoryID string `json:"repository_id"`
	}
	if err := decodeJSON(w, r, &input, 8<<10); err != nil {
		return
	}
	s.mu.Lock()
	if s.syncing {
		s.mu.Unlock()
		writeError(w, 409, "sync_in_progress", "已有同步任务运行中")
		return
	}
	s.syncing = true
	s.mu.Unlock()
	go func() {
		defer func() { s.mu.Lock(); s.syncing = false; s.mu.Unlock() }()
		s.syncRepositories(context.Background(), input.RepositoryID, false)
	}()
	writeJSON(w, 202, map[string]string{"status": "accepted"})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method_not_allowed", "仅支持 GET")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	writeJSON(w, 200, s.store.events(limit))
}

func (s *Server) runScheduler(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	s.syncRepositories(ctx, "", true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncRepositories(ctx, "", true)
		}
	}
}

func (s *Server) syncRepositories(ctx context.Context, only string, scheduled bool) {
	now := time.Now()
	interval := time.Duration(s.store.Config().ScheduleMinute) * time.Minute
	if interval < 5*time.Minute {
		interval = time.Hour
	}
	for _, repo := range s.store.repositories() {
		if !repo.Enabled || (only != "" && repo.ID != only) {
			continue
		}
		if scheduled {
			if !repo.NextRetry.IsZero() && now.Before(repo.NextRetry) {
				continue
			}
			if repo.NextRetry.IsZero() && !repo.LastAttempt.IsZero() && now.Sub(repo.LastAttempt) < interval {
				continue
			}
		}
		repo.LastAttempt = now
		if private, err := s.github.RepositoryPrivate(ctx, repo.Owner, strings.TrimPrefix(repo.FullName, repo.Owner+"/")); err != nil || !private {
			repo.Health, repo.Enabled = "public_or_unverifiable", false
			if err != nil {
				repo.Error = err.Error()
			} else {
				repo.Error = "仓库已公开，自动停用"
			}
			_ = s.store.upsertRepository(repo)
			continue
		}
		if err := s.git.SyncRepository(ctx, repo); err != nil {
			repo.Health, repo.Error = "error", err.Error()
			repo.RetryCount++
			repo.NextRetry = now.Add(retryDelay(repo.RetryCount))
			s.store.log("error", "sync_failed", repo.ID, err.Error())
		} else {
			repo.Health, repo.Error, repo.LastSync = "healthy", "", time.Now()
			repo.RetryCount, repo.NextRetry = 0, time.Time{}
			s.store.log("info", "sync_succeeded", repo.ID, repo.FullName)
		}
		_ = s.store.upsertRepository(repo)
	}
}

type contextKey string

const sessionContextKey contextKey = "session"

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("gna_session")
		if err != nil {
			writeError(w, 401, "authentication_required", "请先登录")
			return
		}
		s.mu.Lock()
		session, ok := s.sessions[cookie.Value]
		now := time.Now()
		if ok && (now.Sub(session.LastSeen) > 30*time.Minute || now.Sub(session.CreatedAt) > 8*time.Hour) {
			delete(s.sessions, cookie.Value)
			ok = false
		}
		if ok {
			session.LastSeen = now
		}
		s.mu.Unlock()
		if !ok {
			writeError(w, 401, "session_expired", "会话已过期")
			return
		}
		w.Header().Set("X-CSRF-Token", session.CSRF)
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRF)) != 1 || !s.validOrigin(r) {
				writeError(w, 403, "csrf_rejected", "请求来源或 CSRF token 无效")
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionContextKey, session.ID)))
	}
}

func (s *Server) validOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || !strings.EqualFold(u.Host, r.Host) {
		return false
	}
	if s.isTrustedHost(requestHost(r.Host)) {
		return u.Scheme == "https"
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

func sessionIDFrom(ctx context.Context) string {
	value, _ := ctx.Value(sessionContextKey).(string)
	return value
}

func (s *Server) getDiscovery(id, sessionID string) (*DiscoverySession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.discoveries[id]
	if !ok || item.AdminSession != sessionID || time.Now().After(item.ExpiresAt) {
		if ok {
			delete(s.discoveries, id)
		}
		return nil, false
	}
	return item, true
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := requestHost(r.Host)
		if !isLoopbackHost(host) && !s.isTrustedHost(host) {
			writeError(w, 400, "invalid_host", "Host 不在允许列表")
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func requestHost(value string) string {
	if parsed, _, err := net.SplitHostPort(value); err == nil {
		value = parsed
	}
	return strings.TrimSuffix(strings.ToLower(strings.Trim(value, "[]")), ".")
}

func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func (s *Server) isTrustedHost(host string) bool {
	_, ok := s.trustedHosts[host]
	return ok
}

func parseTrustedHosts(value string) map[string]struct{} {
	hosts := make(map[string]struct{})
	for _, value := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		host := requestHost(value)
		if host != "" && len(host) <= 253 && !strings.ContainsAny(host, "/\\@") {
			hosts[host] = struct{}{}
		}
	}
	return hosts
}

func decodeJSON(w http.ResponseWriter, r *http.Request, output any, max int64) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, 415, "content_type_required", "必须使用 application/json")
		return errors.New("content type")
	}
	r.Body = http.MaxBytesReader(w, r.Body, max)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(output); err != nil {
		writeError(w, 400, "invalid_json", "JSON 请求无效")
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, 400, "invalid_json", "JSON 只能包含一个对象")
		return errors.New("trailing json")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func countEnabled(repos []RepositoryConfig) int {
	count := 0
	for _, repo := range repos {
		if repo.Enabled {
			count++
		}
	}
	return count
}

func retryDelay(count int) time.Duration {
	delays := []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour}
	if count < 1 {
		count = 1
	}
	if count > len(delays) {
		count = len(delays)
	}
	return delays[count-1]
}

var _ = fmt.Sprintf
