package app

import "time"

const (
	defaultListen      = "127.0.0.1:17891"
	defaultArchivePath = "activity-notes"
)

type Config struct {
	Listen         string             `json:"listen"`
	Timezone       string             `json:"timezone"`
	AuthorName     string             `json:"author_name"`
	AuthorEmail    string             `json:"author_email"`
	TokenHash      string             `json:"token_hash"`
	Repositories   []RepositoryConfig `json:"repositories"`
	ScheduleMinute int                `json:"schedule_minute"`
}

type RepositoryConfig struct {
	ID            string    `json:"id"`
	FullName      string    `json:"full_name"`
	Owner         string    `json:"owner"`
	DefaultBranch string    `json:"default_branch"`
	ArchivePath   string    `json:"archive_path"`
	AuthMode      string    `json:"auth_mode"`
	KeyPath       string    `json:"key_path,omitempty"`
	RemoteKeyID   int64     `json:"remote_key_id,omitempty"`
	Enabled       bool      `json:"enabled"`
	Health        string    `json:"health"`
	LastSync      time.Time `json:"last_sync,omitempty"`
	LastAttempt   time.Time `json:"last_attempt,omitempty"`
	NextRetry     time.Time `json:"next_retry,omitempty"`
	RetryCount    int       `json:"retry_count,omitempty"`
	Error         string    `json:"error,omitempty"`
	CloneURL      string    `json:"clone_url"`
	WebURL        string    `json:"web_url"`
}

type ArchiveEvent struct {
	ID            string    `json:"id"`
	RepositoryID  string    `json:"repository_id"`
	LogicalPath   string    `json:"logical_path"`
	Title         string    `json:"title"`
	ContentSHA256 string    `json:"content_sha256"`
	VersionAt     time.Time `json:"version_at"`
	Source        string    `json:"source"`
	Status        string    `json:"status"`
	Error         string    `json:"error,omitempty"`
	Content       string    `json:"content,omitempty"`
}

type GitHubRepository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	Private       bool   `json:"private"`
	Fork          bool   `json:"fork"`
	Archived      bool   `json:"archived"`
	DefaultBranch string `json:"default_branch"`
	WebURL        string `json:"web_url"`
	SSHURL        string `json:"ssh_url"`
	Admin         bool   `json:"admin"`
	Eligible      bool   `json:"eligible"`
	DisabledWhy   string `json:"disabled_why,omitempty"`
	OwnerType     string `json:"owner_type"`
}

type DiscoverySession struct {
	ID           string
	AdminSession string
	Username     string
	Owner        string
	Token        string
	Repositories []GitHubRepository
	ExpiresAt    time.Time
}

type adminSession struct {
	ID        string
	CSRF      string
	CreatedAt time.Time
	LastSeen  time.Time
}

type ImportVersion struct {
	LogicalPath string    `json:"logical_path"`
	Title       string    `json:"title"`
	Content     string    `json:"-"`
	SHA256      string    `json:"sha256"`
	VersionAt   time.Time `json:"version_at"`
	Line        int       `json:"line"`
}

type ImportPreview struct {
	ID           string          `json:"id"`
	AdminSession string          `json:"-"`
	RepositoryID string          `json:"repository_id"`
	PackageHash  string          `json:"package_sha256"`
	BaseHead     string          `json:"base_head"`
	Versions     []ImportVersion `json:"versions"`
	Warnings     []string        `json:"warnings"`
	CreatedAt    time.Time       `json:"created_at"`
	ExpiresAt    time.Time       `json:"expires_at"`
}

type EventLog struct {
	Time         time.Time `json:"time"`
	Level        string    `json:"level"`
	Kind         string    `json:"kind"`
	RepositoryID string    `json:"repository_id,omitempty"`
	Message      string    `json:"message"`
}
