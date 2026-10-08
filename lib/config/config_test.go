package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validYAML(vault, logPath, size string) string {
	return yamlWithIndex(vault, logPath, size, filepath.Join(filepath.Dir(logPath), "index.sqlite"))
}

func yamlWithIndex(vault, logPath, size, indexPath string) string {
	return "vault: " + vault + "\nsearch_size: " + size + "\nlog_path: " + logPath + "\nindex_path: " + indexPath + "\n"
}

func TestLoadReturnsValidatedConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, validYAML(dir, filepath.Join(dir, "app.log"), "1GB"))

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Vault != dir || cfg.SearchSize != "1GB" || cfg.LogPath != filepath.Join(dir, "app.log") || cfg.IndexPath != filepath.Join(dir, "index.sqlite") {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{"zero search size", validYAML(dir, logPath, "0GB"), "search_size"},
		{"missing suffix", validYAML(dir, logPath, "100"), "search_size"},
		{"relative vault", validYAML("relative/vault", logPath, "1GB"), "vault must be an absolute path"},
		{"empty vault", "search_size: 1GB\nlog_path: " + logPath + "\nindex_path: " + filepath.Join(dir, "index.sqlite") + "\n", "vault must be an absolute path"},
		{"missing vault dir", validYAML(filepath.Join(dir, "missing"), logPath, "1GB"), "vault:"},
		{"vault is a file", validYAML(file, logPath, "1GB"), "vault must be a directory"},
		{"relative log path", validYAML(dir, "app.log", "1GB"), "log_path must be an absolute path"},
		{"empty index path", "vault: " + dir + "\nsearch_size: 1GB\nlog_path: " + logPath + "\n", "index_path must be an absolute path"},
		{"relative index path", yamlWithIndex(dir, logPath, "1GB", "index.sqlite"), "index_path must be an absolute path"},
		{"index inside Knowledge", yamlWithIndex(dir, logPath, "1GB", filepath.Join(dir, "Knowledge", "index.sqlite")), "outside the Knowledge"},
		{"malformed yaml", "vault: [unclosed", "parse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.content))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "config.yaml"))
	if err == nil || !strings.Contains(err.Error(), "config.yaml") {
		t.Fatalf("error = %v, want one mentioning config.yaml", err)
	}
}

func TestValidateSizeAcceptsAllUnits(t *testing.T) {
	for _, size := range []string{"1B", "5KB", "10mb", "2GB", "1TB", "3 MB"} {
		if err := validateSize(size); err != nil {
			t.Errorf("validateSize(%q) = %v", size, err)
		}
	}
}
