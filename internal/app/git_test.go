package app

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGitEnvSupportsLegacyGit(t *testing.T) {
	keyPath := filepath.Join("data", "keys", "repo", "id_ed25519")
	env := strings.Join(gitEnv(keyPath, "", ""), "\n")
	for _, expected := range []string{"GIT_SSH=", "GIT_SSH_COMMAND=", "GNA_SSH_KEY_PATH=" + keyPath, "GNA_SSH_KNOWN_HOSTS="} {
		if !strings.Contains(env, expected) {
			t.Fatalf("git environment missing %q", expected)
		}
	}
}
