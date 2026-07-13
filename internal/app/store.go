package app

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Store struct {
	mu      sync.RWMutex
	dataDir string
	logDir  string
	config  Config
	logs    []EventLog
}

func OpenStore(dataDir, logDir string) (*Store, string, error) {
	for _, dir := range []string{dataDir, logDir, filepath.Join(dataDir, "repos"), filepath.Join(dataDir, "keys"), filepath.Join(dataDir, "queue"), filepath.Join(dataDir, "imports")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, "", err
		}
	}
	s := &Store{dataDir: dataDir, logDir: logDir}
	path := filepath.Join(dataDir, "config.json")
	b, err := os.ReadFile(path)
	var token string
	if errors.Is(err, os.ErrNotExist) {
		token, err = randomToken(32)
		if err != nil {
			return nil, "", err
		}
		s.config = Config{Listen: defaultListen, Timezone: "Asia/Shanghai", ScheduleMinute: 60, TokenHash: hashToken(token)}
		if err := s.saveConfigLocked(); err != nil {
			return nil, "", err
		}
		if err := atomicWrite(filepath.Join(dataDir, "initial-admin-token"), []byte(token+"\n"), 0600); err != nil {
			return nil, "", err
		}
	} else if err != nil {
		return nil, "", err
	} else if err := json.Unmarshal(b, &s.config); err != nil {
		return nil, "", fmt.Errorf("读取配置失败: %w", err)
	}
	if s.config.Listen == "" {
		s.config.Listen = defaultListen
	}
	return s, token, nil
}

func (s *Store) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.config
	c.TokenHash = ""
	c.Repositories = append([]RepositoryConfig(nil), s.config.Repositories...)
	return c
}

func (s *Store) verifyToken(token string) bool {
	s.mu.RLock()
	hash := s.config.TokenHash
	s.mu.RUnlock()
	return subtle.ConstantTimeCompare([]byte(hash), []byte(hashToken(token))) == 1
}

func (s *Store) rotateToken() (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config.TokenHash = hashToken(token)
	if err := s.saveConfigLocked(); err != nil {
		return "", err
	}
	return token, atomicWrite(filepath.Join(s.dataDir, "initial-admin-token"), []byte(token+"\n"), 0600)
}

func (s *Store) updateConfig(patch Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if patch.Timezone != "" {
		if _, err := time.LoadLocation(patch.Timezone); err != nil {
			return errors.New("无效时区")
		}
		s.config.Timezone = patch.Timezone
	}
	if patch.AuthorName != "" {
		s.config.AuthorName = strings.TrimSpace(patch.AuthorName)
	}
	if patch.AuthorEmail != "" {
		email := strings.TrimSpace(patch.AuthorEmail)
		if !strings.Contains(email, "@") || strings.ContainsAny(email, "\r\n") {
			return errors.New("无效 Git 作者邮箱")
		}
		s.config.AuthorEmail = email
	}
	if patch.ScheduleMinute >= 5 && patch.ScheduleMinute <= 1440 {
		s.config.ScheduleMinute = patch.ScheduleMinute
	}
	return s.saveConfigLocked()
}

func (s *Store) repositories() []RepositoryConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]RepositoryConfig(nil), s.config.Repositories...)
}

func (s *Store) repository(id string) (RepositoryConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, repo := range s.config.Repositories {
		if repo.ID == id {
			return repo, true
		}
	}
	return RepositoryConfig{}, false
}

func (s *Store) upsertRepository(repo RepositoryConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.config.Repositories {
		if s.config.Repositories[i].ID == repo.ID {
			s.config.Repositories[i] = repo
			return s.saveConfigLocked()
		}
	}
	s.config.Repositories = append(s.config.Repositories, repo)
	return s.saveConfigLocked()
}

func (s *Store) saveEvent(event ArchiveEvent) error {
	dir := filepath.Join(s.dataDir, "queue", event.RepositoryID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, event.ID+".json"), mustJSON(event), 0600)
}

func (s *Store) queue(repositoryID string) ([]ArchiveEvent, error) {
	dir := filepath.Join(s.dataDir, "queue", repositoryID)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result []ArchiveEvent
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var event ArchiveEvent
		if err := json.Unmarshal(b, &event); err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, nil
}

func (s *Store) removeEvent(event ArchiveEvent) error {
	return os.Remove(filepath.Join(s.dataDir, "queue", event.RepositoryID, event.ID+".json"))
}

func (s *Store) log(level, kind, repoID, message string) {
	entry := EventLog{Time: time.Now(), Level: level, Kind: kind, RepositoryID: repoID, Message: redact(message)}
	s.mu.Lock()
	s.logs = append(s.logs, entry)
	if len(s.logs) > 500 {
		s.logs = append([]EventLog(nil), s.logs[len(s.logs)-500:]...)
	}
	s.mu.Unlock()
	_ = appendJSONL(filepath.Join(s.logDir, "app.jsonl"), entry)
}

func (s *Store) events(limit int) []EventLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	start := len(s.logs) - limit
	if start < 0 {
		start = 0
	}
	result := append([]EventLog(nil), s.logs[start:]...)
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

func (s *Store) saveConfigLocked() error {
	return atomicWrite(filepath.Join(s.dataDir, "config.json"), mustJSON(s.config), 0600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func appendJSONL(path string, value any) error {
	if info, err := os.Stat(path); err == nil && info.Size() >= 10<<20 {
		for i := 4; i >= 1; i-- {
			old := fmt.Sprintf("%s.%d", path, i)
			if i == 4 {
				_ = os.Remove(old)
			}
			_ = os.Rename(fmt.Sprintf("%s.%d", path, i-1), old)
		}
		_ = os.Rename(path, path+".0")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(value)
}

func loadJSONL[T any](r io.Reader) ([]T, error) {
	var result []T
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		var value T
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, scanner.Err()
}

func randomToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func mustJSON(value any) []byte {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

func redact(value string) string {
	for _, marker := range []string{"github_pat_", "ghp_", "-----BEGIN OPENSSH PRIVATE KEY-----"} {
		if i := strings.Index(value, marker); i >= 0 {
			return value[:i] + "[REDACTED]"
		}
	}
	return value
}
