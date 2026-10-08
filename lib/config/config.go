package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

type Config struct {
	Vault      string `yaml:"vault"`
	SearchSize string `yaml:"search_size"`
	LogPath    string `yaml:"log_path"`
	IndexPath  string `yaml:"index_path"`
}

// Load reads and validates the YAML configuration file at path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks that every setting is present and usable.
func (c Config) Validate() error {
	if c.Vault == "" || !filepath.IsAbs(c.Vault) {
		return fmt.Errorf("vault must be an absolute path")
	}
	info, err := os.Stat(c.Vault)
	if err != nil {
		return fmt.Errorf("vault: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("vault must be a directory")
	}
	if c.LogPath == "" || !filepath.IsAbs(c.LogPath) {
		return fmt.Errorf("log_path must be an absolute path")
	}
	if c.IndexPath == "" || !filepath.IsAbs(c.IndexPath) {
		return fmt.Errorf("index_path must be an absolute path")
	}
	if isWithin(filepath.Join(c.Vault, "Knowledge"), c.IndexPath) {
		return fmt.Errorf("index_path must be outside the Knowledge directory")
	}
	if err := validateSize(c.SearchSize); err != nil {
		return fmt.Errorf("search_size: %w", err)
	}
	return nil
}

// isWithin reports whether path is dir itself or lies beneath it.
func isWithin(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validateSize(value string) error {
	for _, suffix := range []string{"KB", "MB", "GB", "TB", "B"} {
		if strings.HasSuffix(strings.ToUpper(value), suffix) {
			number := strings.TrimSpace(value[:len(value)-len(suffix)])
			n, err := strconv.ParseUint(number, 10, 64)
			if err == nil && n > 0 {
				return nil
			}
			break
		}
	}
	return fmt.Errorf("must be a positive size with a B, KB, MB, GB, or TB suffix")
}
