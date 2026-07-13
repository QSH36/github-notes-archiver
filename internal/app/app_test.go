package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSafeLogicalPath(t *testing.T) {
	valid := []string{"learning/go.md", "todo/2026-07.md", "中文/笔记.md"}
	for _, value := range valid {
		if _, err := safeLogicalPath(value); err != nil {
			t.Fatalf("expected %q valid: %v", value, err)
		}
	}
	invalid := []string{"../secret", "/etc/passwd", "a/../../b", "", `C:\\secret`}
	for _, value := range invalid {
		if _, err := safeLogicalPath(value); err == nil {
			t.Fatalf("expected %q invalid", value)
		}
	}
}

func TestBranchAndSymlinkValidation(t *testing.T) {
	for _, branch := range []string{"main", "release/2026.07"} {
		if !safeBranch(branch) {
			t.Fatalf("valid branch rejected: %s", branch)
		}
	}
	for _, branch := range []string{"-force", "a..b", "a@{b", "a.lock", "a/"} {
		if safeBranch(branch) {
			t.Fatalf("invalid branch accepted: %s", branch)
		}
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "activity-notes")); err == nil {
		if err := rejectSymlinkParents(root, filepath.Join("activity-notes", "note.md")); err == nil {
			t.Fatal("symlink parent accepted")
		}
	}
}

func TestCSVImportAndDuplicateRejection(t *testing.T) {
	data := "logical_path,version_at,title,content\nnotes/a.md,2026-07-01T08:00:00+08:00,Start,hello\nnotes/a.md,2026-07-02T08:00:00+08:00,Next,world\n"
	versions, err := parseImportCSV(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].Content != "hello" || versions[0].SHA256 != contentHash("hello") {
		t.Fatalf("unexpected versions: %#v", versions)
	}
	duplicate := "logical_path,version_at,content\na.md,2026-07-01T08:00:00+08:00,a\na.md,2026-07-01T08:00:00+08:00,b\n"
	if _, err := parseImportCSV(strings.NewReader(duplicate)); err == nil {
		t.Fatal("expected duplicate rejection")
	}
}

func TestZIPImportValidatesHashesAndPaths(t *testing.T) {
	content := []byte("# real note\n")
	manifest := "logical_path,version_at,content_file,sha256,title\nnotes/a.md,2026-07-01T08:00:00+08:00,files/a.md," + contentHash(string(content)) + ",A\n"
	data := makeZIP(t, map[string][]byte{"manifest.csv": []byte(manifest), "files/a.md": content})
	versions, err := parseImportZIP(data)
	if err != nil || len(versions) != 1 {
		t.Fatalf("valid zip rejected: %v %#v", err, versions)
	}
	bad := makeZIP(t, map[string][]byte{"manifest.csv": []byte(strings.Replace(manifest, contentHash(string(content)), strings.Repeat("0", 64), 1)), "files/a.md": content})
	if _, err := parseImportZIP(bad); err == nil {
		t.Fatal("expected hash mismatch")
	}
}

func TestGitHubPaginationAndEligibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("token missing")
		}
		if r.URL.Query().Get("page") == "2" {
			io.WriteString(w, `[{"id":2,"full_name":"me/public","name":"public","private":false,"default_branch":"main","html_url":"x","ssh_url":"git@github.com:me/public.git","owner":{"login":"me","type":"User"},"permissions":{"admin":true}}]`)
			return
		}
		w.Header().Set("Link", `<`+serverURL(r)+`/user/repos?page=2>; rel="next"`)
		io.WriteString(w, `[{"id":1,"full_name":"me/private","name":"private","private":true,"default_branch":"main","html_url":"x","ssh_url":"git@github.com:me/private.git","owner":{"login":"me","type":"User"},"permissions":{"admin":true}}]`)
	}))
	defer server.Close()
	client := NewGitHubClient()
	client.BaseURL = server.URL
	client.Client = server.Client()
	repos, err := client.ListRepositories(context.Background(), "secret", "me")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || !repos[0].Eligible || repos[1].Eligible {
		t.Fatalf("unexpected eligibility: %#v", repos)
	}
}

func TestDeployKeyCreationIsIdempotent(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		io.WriteString(w, `[{"id":42,"title":"github-notes-archiver 1","key":"ssh-ed25519 AAA test","read_only":false}]`)
	}))
	defer server.Close()
	client := NewGitHubClient()
	client.BaseURL = server.URL
	client.Client = server.Client()
	repo := GitHubRepository{Owner: "me", Name: "repo"}
	id, err := client.CreateDeployKey(context.Background(), "secret", repo, "github-notes-archiver 1", "ssh-ed25519 AAA test\n")
	if err != nil || id != 42 || requests != 1 {
		t.Fatalf("idempotent key lookup failed: id=%d requests=%d err=%v", id, requests, err)
	}
}

func TestSessionCSRFAndSecretNotPersisted(t *testing.T) {
	store, token := testStore(t)
	server := NewServer(store, "test")
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	loginBody := strings.NewReader(`{"token":"` + token + `"}`)
	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/session", loginBody)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("login failed: %v %v", err, resp.Status)
	}
	var login map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&login)
	resp.Body.Close()
	cookie := resp.Cookies()[0]

	req, _ = http.NewRequest(http.MethodPut, httpServer.URL+"/api/v1/config", strings.NewReader(`{"author_name":"Me"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("write without csrf/origin accepted: %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPut, httpServer.URL+"/api/v1/config", strings.NewReader(`{"author_name":"Me","author_email":"me@example.com"}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", login["csrf_token"])
	req.Header.Set("Origin", httpServer.URL)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid csrf rejected: %d", resp.StatusCode)
	}
	configBytes, _ := os.ReadFile(filepath.Join(store.dataDir, "config.json"))
	if bytes.Contains(configBytes, []byte(token)) {
		t.Fatal("raw admin token persisted")
	}
}

func TestTrustedPublicHostRequiresHTTPS(t *testing.T) {
	store, token := testStore(t)
	t.Setenv("GNA_TRUSTED_HOSTS", "notes.example.com")
	server := NewServer(store, "test")

	req := httptest.NewRequest(http.MethodGet, "https://notes.example.com/healthz", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("trusted host rejected: %d", response.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "https://notes.example.com.attacker.test/healthz", nil)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("untrusted host accepted: %d", response.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "https://notes.example.com/api/v1/session", strings.NewReader(`{"token":"`+token+`"}`))
	req.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusCreated || !response.Result().Cookies()[0].Secure {
		t.Fatalf("trusted host session cookie is not secure")
	}

	req = httptest.NewRequest(http.MethodPost, "https://notes.example.com/api/v1/config", nil)
	req.Header.Set("Origin", "http://notes.example.com")
	if server.validOrigin(req) {
		t.Fatal("trusted host accepted insecure origin")
	}
}

func TestStoreQueueRoundTrip(t *testing.T) {
	store, _ := testStore(t)
	event := ArchiveEvent{ID: "event", RepositoryID: "repo", LogicalPath: "a.md", Content: "secret正文", ContentSHA256: contentHash("secret正文"), VersionAt: time.Now(), Status: "queued"}
	if err := store.saveEvent(event); err != nil {
		t.Fatal(err)
	}
	items, err := store.queue("repo")
	if err != nil || len(items) != 1 || items[0].Content != event.Content {
		t.Fatalf("queue mismatch: %v %#v", err, items)
	}
}

func TestRetryDelayCapsAtThreeHours(t *testing.T) {
	want := []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, 3 * time.Hour, 3 * time.Hour}
	for i, expected := range want {
		if actual := retryDelay(i + 1); actual != expected {
			t.Fatalf("retry %d: got %s want %s", i+1, actual, expected)
		}
	}
}

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	store, token, err := OpenStore(filepath.Join(dir, "data"), filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	return store, token
}

func makeZIP(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func serverURL(r *http.Request) string { return "http://" + r.Host }

var _ = csv.ErrFieldCount
