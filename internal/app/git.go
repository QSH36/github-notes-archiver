package app

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

var (
	fullNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	branchPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
)

type GitManager struct {
	store *Store
	mu    sync.Mutex
}

func NewGitManager(store *Store) *GitManager { return &GitManager{store: store} }

func (g *GitManager) GenerateDeployKey(ctx context.Context, repoID string) (string, string, error) {
	keyPath := filepath.Join(g.store.dataDir, "keys", repoID, "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return "", "", err
	}
	if _, err := os.Stat(keyPath); errors.Is(err, os.ErrNotExist) {
		if _, err := run(ctx, "", nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "github-notes-archiver:"+repoID, "-f", keyPath); err != nil {
			return "", "", err
		}
		_ = os.Chmod(keyPath, 0600)
		_ = os.Chmod(keyPath+".pub", 0644)
	}
	publicKey, err := os.ReadFile(keyPath + ".pub")
	return keyPath, string(publicKey), err
}

func (g *GitManager) SavePrivateKey(ctx context.Context, repoID, privateKey string) (string, error) {
	if !strings.Contains(privateKey, "BEGIN OPENSSH PRIVATE KEY") || len(privateKey) > 64<<10 {
		return "", errors.New("仅支持无口令 OpenSSH 私钥")
	}
	keyPath := filepath.Join(g.store.dataDir, "keys", repoID, "id_uploaded")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return "", err
	}
	if err := atomicWrite(keyPath, []byte(strings.TrimSpace(privateKey)+"\n"), 0600); err != nil {
		return "", err
	}
	if _, err := run(ctx, "", nil, "ssh-keygen", "-y", "-P", "", "-f", keyPath); err != nil {
		_ = os.Remove(keyPath)
		return "", errors.New("私钥无效或带有口令")
	}
	return keyPath, nil
}

func (g *GitManager) TestAndClone(ctx context.Context, repo RepositoryConfig) error {
	expectedURL := "git@github.com:" + repo.FullName + ".git"
	if !fullNamePattern.MatchString(repo.FullName) || !safeBranch(repo.DefaultBranch) || !strings.EqualFold(repo.CloneURL, expectedURL) {
		return errors.New("仅允许 GitHub SSH 仓库地址")
	}
	workDir := filepath.Join(g.store.dataDir, "repos", repo.ID)
	env := gitEnv(repo.KeyPath, "", "")
	if _, err := run(ctx, "", env, "git", "ls-remote", "--exit-code", repo.CloneURL, "refs/heads/"+repo.DefaultBranch); err != nil {
		return fmt.Errorf("SSH 或默认分支验证失败: %w", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(workDir), 0700); err != nil {
			return err
		}
		if _, err := run(ctx, "", env, "git", "clone", "--single-branch", "--branch", repo.DefaultBranch, "--", repo.CloneURL, workDir); err != nil {
			return err
		}
	}
	archiveDir := filepath.Join(workDir, repo.ArchivePath)
	if err := rejectSymlinkParents(workDir, filepath.Join(repo.ArchivePath, ".archive", "manifest.jsonl")); err != nil {
		return err
	}
	if entries, err := os.ReadDir(archiveDir); err == nil && len(entries) > 0 {
		if _, err := os.Stat(filepath.Join(archiveDir, ".archive", "manifest.jsonl")); errors.Is(err, os.ErrNotExist) {
			return errors.New("归档目录已存在且不受本程序管理，请配置其他空目录")
		}
	}
	return nil
}

func (g *GitManager) Head(ctx context.Context, repo RepositoryConfig) (string, error) {
	workDir := filepath.Join(g.store.dataDir, "repos", repo.ID)
	output, err := run(ctx, workDir, gitEnv(repo.KeyPath, "", ""), "git", "rev-parse", "HEAD")
	return strings.TrimSpace(output), err
}

func (g *GitManager) SyncRepository(ctx context.Context, repo RepositoryConfig) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !repo.Enabled {
		return errors.New("仓库未启用")
	}
	if err := g.TestAndClone(ctx, repo); err != nil {
		return err
	}
	workDir := filepath.Join(g.store.dataDir, "repos", repo.ID)
	env := gitEnv(repo.KeyPath, "", "")
	config := g.store.Config()
	if config.AuthorName == "" || config.AuthorEmail == "" {
		return errors.New("尚未配置 Git 作者姓名和已验证邮箱")
	}
	status, err := run(ctx, workDir, env, "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(status), "\n") {
		if line == "" {
			continue
		}
		changed := strings.TrimSpace(strings.TrimPrefix(line[2:], `"`))
		changed = strings.TrimSuffix(changed, `"`)
		if !strings.HasPrefix(filepath.ToSlash(changed), strings.TrimSuffix(repo.ArchivePath, "/")+"/") {
			return fmt.Errorf("工作区存在归档目录外的改动，已暂停: %s", changed)
		}
	}
	if _, err := run(ctx, workDir, env, "git", "fetch", "origin", repo.DefaultBranch); err != nil {
		return err
	}
	local, err := run(ctx, workDir, env, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	remote, err := run(ctx, workDir, env, "git", "rev-parse", "origin/"+repo.DefaultBranch)
	if err != nil {
		return err
	}
	base, err := run(ctx, workDir, env, "git", "merge-base", "HEAD", "origin/"+repo.DefaultBranch)
	if err != nil {
		return err
	}
	local, remote, base = strings.TrimSpace(local), strings.TrimSpace(remote), strings.TrimSpace(base)
	if local != remote {
		switch {
		case local == base:
			if _, err := run(ctx, workDir, env, "git", "merge", "--ff-only", "origin/"+repo.DefaultBranch); err != nil {
				return err
			}
		case remote != base:
			return errors.New("本地与远端已分叉，已暂停且不会强推")
		}
	}
	events, err := g.store.queue(repo.ID)
	if err != nil {
		return err
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].VersionAt.Before(events[j].VersionAt) })
	for _, event := range events {
		if eventExistsInGit(ctx, workDir, event.ID) {
			if err := g.store.removeEvent(event); err != nil {
				return err
			}
			continue
		}
		if err := g.commitEvent(ctx, workDir, repo, event); err != nil {
			return err
		}
		if err := g.store.removeEvent(event); err != nil {
			return err
		}
	}
	if _, err := run(ctx, workDir, env, "git", "push", "origin", "HEAD:"+repo.DefaultBranch); err != nil {
		return err
	}
	return nil
}

func (g *GitManager) commitEvent(ctx context.Context, workDir string, repo RepositoryConfig, event ArchiveEvent) error {
	relative, err := safeLogicalPath(event.LogicalPath)
	if err != nil {
		return err
	}
	target := filepath.Join(workDir, repo.ArchivePath, relative)
	manifest := filepath.Join(workDir, repo.ArchivePath, ".archive", "manifest.jsonl")
	if err := rejectSymlinkParents(workDir, filepath.Join(repo.ArchivePath, relative)); err != nil {
		return err
	}
	if err := rejectSymlinkParents(workDir, filepath.Join(repo.ArchivePath, ".archive", "manifest.jsonl")); err != nil {
		return err
	}
	manifestHasEvent := fileContains(manifest, event.ID)
	if existing, err := os.ReadFile(target); err == nil && contentHash(string(existing)) == event.ContentSHA256 && !manifestHasEvent {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	if err := atomicWrite(target, []byte(event.Content), 0600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(manifest), 0700); err != nil {
		return err
	}
	if !manifestHasEvent {
		if err := appendJSONL(manifest, map[string]any{"event_id": event.ID, "logical_path": event.LogicalPath, "sha256": event.ContentSHA256, "version_at": event.VersionAt, "source": event.Source}); err != nil {
			return err
		}
	}
	cfg := g.store.Config()
	env := gitEnv(repo.KeyPath, event.VersionAt.Format(time.RFC3339), event.VersionAt.Format(time.RFC3339))
	env = append(env, "GIT_AUTHOR_NAME="+cfg.AuthorName, "GIT_AUTHOR_EMAIL="+cfg.AuthorEmail, "GIT_COMMITTER_NAME="+cfg.AuthorName, "GIT_COMMITTER_EMAIL="+cfg.AuthorEmail)
	if _, err := run(ctx, workDir, env, "git", "add", "--", filepath.ToSlash(filepath.Join(repo.ArchivePath, relative)), filepath.ToSlash(filepath.Join(repo.ArchivePath, ".archive", "manifest.jsonl"))); err != nil {
		return err
	}
	staged, err := run(ctx, workDir, env, "git", "diff", "--cached", "--quiet")
	if err == nil && staged == "" {
		return nil
	}
	message := fmt.Sprintf("docs: archive %s\n\nArchive-Event: %s\nContent-SHA256: %s", event.LogicalPath, event.ID, event.ContentSHA256)
	_, err = run(ctx, workDir, env, "git", "commit", "-m", message)
	return err
}

func safeLogicalPath(value string) (string, error) {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" || len([]rune(value)) > 200 || strings.HasPrefix(value, "/") || strings.Contains(value, "../") || value == ".." {
		return "", errors.New("无效逻辑路径")
	}
	for _, character := range value {
		if unicode.IsControl(character) || character == '\\' || strings.ContainsRune(`:*?"<>|`, character) {
			return "", errors.New("无效逻辑路径")
		}
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", errors.New("无效逻辑路径")
	}
	return clean, nil
}

func safeBranch(value string) bool {
	return branchPattern.MatchString(value) && !strings.Contains(value, "..") && !strings.Contains(value, "@{") && !strings.HasSuffix(value, ".") && !strings.HasSuffix(value, "/") && !strings.HasSuffix(value, ".lock")
}

func rejectSymlinkParents(root, relative string) error {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	cleanRelative := filepath.Clean(relative)
	if filepath.IsAbs(cleanRelative) || strings.HasPrefix(cleanRelative, "..") {
		return errors.New("工作树路径越界")
	}
	current := cleanRoot
	parts := strings.Split(cleanRelative, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("拒绝跟随工作树符号链接: %s", part)
		}
	}
	return nil
}

func gitEnv(keyPath, authorDate, committerDate string) []string {
	knownHosts := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(keyPath))), "known_hosts")
	executable, _ := os.Executable()
	wrapper := filepath.Join(filepath.Dir(executable), "git-ssh-wrapper")
	env := []string{
		"GIT_SSH=" + wrapper,
		"GIT_SSH_COMMAND=" + shellQuote(wrapper),
		"GNA_SSH_KEY_PATH=" + keyPath,
		"GNA_SSH_KNOWN_HOSTS=" + knownHosts,
		"GIT_TERMINAL_PROMPT=0",
	}
	if authorDate != "" {
		env = append(env, "GIT_AUTHOR_DATE="+authorDate)
	}
	if committerDate != "" {
		env = append(env, "GIT_COMMITTER_DATE="+committerDate)
	}
	return env
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func run(ctx context.Context, dir string, extraEnv []string, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", fmt.Errorf("命令超时: %s", name)
	}
	if err != nil {
		return "", fmt.Errorf("%s 失败: %s", name, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func eventExistsInGit(ctx context.Context, workDir, eventID string) bool {
	cmd := exec.CommandContext(ctx, "git", "log", "--all", "--fixed-strings", "--grep=Archive-Event: "+eventID, "-1", "--format=%H")
	cmd.Dir = workDir
	output, err := cmd.Output()
	return err == nil && len(strings.TrimSpace(string(output))) > 0
}

func fileContains(path, value string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), value) {
			return true
		}
	}
	return false
}

func readLastLine(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var last string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		last = scanner.Text()
	}
	return last, scanner.Err()
}
