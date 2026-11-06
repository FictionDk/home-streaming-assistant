// Package config loads the single JSON configuration file plus the two
// supported environment overrides, and validates required security inputs.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AliyunConfig is the Alist-backed source configuration.
type AliyunConfig struct {
	BaseURL      string `json:"baseURL"`
	Username     string `json:"username"`
	PasswordFile string `json:"passwordFile"`
	Root         string `json:"root"`
}

// GDriveConfig is the rclone gdrive-ro source configuration. The remote
// name is fixed to "gdrive-ro"; requests never specify it.
type GDriveConfig struct {
	Root string `json:"root"`
}

// JellyfinConfig points at the media server.
type JellyfinConfig struct {
	BaseURL    string            `json:"baseURL"`
	APIKeyFile string            `json:"apiKeyFile"`
	LibraryID  string            `json:"libraryID"`
	WebURLs    map[string]string `json:"webURLs"` // app origin -> jellyfin /web/ URL
}

// Config is the full application configuration.
type Config struct {
	Listen            string         `json:"listen"`
	Origins           []string       `json:"origins"`
	AdminUser         string         `json:"adminUser"`
	AdminPasswordFile string         `json:"adminPasswordFile"`
	StateDir          string         `json:"stateDir"`
	FilesDir          string         `json:"filesDir"`
	RcloneBinary      string         `json:"rcloneBinary"`
	RcloneConfig      string         `json:"rcloneConfig"`
	MinFreeBytes      int64          `json:"minFreeBytes"`
	Aliyun            AliyunConfig   `json:"aliyun"`
	GDrive            GDriveConfig   `json:"gdrive"`
	Jellyfin          JellyfinConfig `json:"jellyfin"`
}

const defaultMinFreeBytes = int64(1073741824)

// Load reads HSA_CONFIG_FILE (default /config/app/config.json) and applies
// HSA_LISTEN_ADDR as the only environment override.
func Load() (*Config, error) {
	path := os.Getenv("HSA_CONFIG_FILE")
	if path == "" {
		path = "/config/app/config.json"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if len(b) > 1<<20 {
		return nil, fmt.Errorf("config file too large")
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if v := os.Getenv("HSA_LISTEN_ADDR"); v != "" {
		cfg.Listen = v
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.AdminUser == "" {
		c.AdminUser = "admin"
	}
	if c.MinFreeBytes == 0 {
		c.MinFreeBytes = defaultMinFreeBytes
	}
	if c.RcloneBinary == "" {
		c.RcloneBinary = "/usr/bin/rclone"
	}
	if c.RcloneConfig == "" {
		c.RcloneConfig = "/config/rclone/rclone.conf"
	}
}

// Validate enforces hard requirements. Missing admin password or illegal
// local storage aborts startup; unconfigured sources are tolerated.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return fmt.Errorf("listen is required")
	}
	if c.AdminPasswordFile == "" {
		return fmt.Errorf("adminPasswordFile is required")
	}
	if fi, err := os.Stat(c.AdminPasswordFile); err != nil {
		return fmt.Errorf("admin password file: %w", err)
	} else if fi.IsDir() {
		return fmt.Errorf("admin password file is a directory")
	}
	if c.StateDir == "" || c.FilesDir == "" {
		return fmt.Errorf("stateDir and filesDir are required")
	}
	if !filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.FilesDir) {
		return fmt.Errorf("stateDir and filesDir must be absolute")
	}
	if filepath.Clean(c.StateDir) == filepath.Clean(c.FilesDir) ||
		strings.HasPrefix(filepath.Clean(c.FilesDir)+string(filepath.Separator), filepath.Clean(c.StateDir)+string(filepath.Separator)) ||
		strings.HasPrefix(filepath.Clean(c.StateDir)+string(filepath.Separator), filepath.Clean(c.FilesDir)+string(filepath.Separator)) {
		return fmt.Errorf("stateDir and filesDir must not overlap")
	}
	if len(c.Origins) == 0 {
		return fmt.Errorf("origins must not be empty")
	}
	for _, o := range c.Origins {
		if !validOrigin(o) {
			return fmt.Errorf("invalid origin %q", o)
		}
	}
	for o := range c.Jellyfin.WebURLs {
		if !validOrigin(o) {
			return fmt.Errorf("invalid webURLs key %q", o)
		}
	}
	// Sources: partial configuration is an error, except the deploy
	// template default where only baseURL is prefilled.
	a := c.Aliyun
	set := 0
	if a.BaseURL != "" {
		set++
	}
	if a.Username != "" {
		set++
	}
	if a.PasswordFile != "" {
		set++
	}
	if a.Root != "" {
		set++
	}
	if set > 0 && set < 4 && !(set == 1 && a.BaseURL != "") {
		return fmt.Errorf("aliyun: baseURL, username, passwordFile and root must all be set when configured")
	}
	if set == 4 && (!strings.HasPrefix(a.Root, "/") || strings.HasSuffix(a.Root, "/")) {
		return fmt.Errorf("aliyun: root must be an absolute path without trailing slash")
	}
	if r := c.GDrive.Root; r != "" {
		if !strings.HasPrefix(r, "/") || strings.HasSuffix(r, "/") {
			return fmt.Errorf("gdrive: root must be an absolute path without trailing slash")
		}
	}
	return nil
}

func validOrigin(o string) bool {
	if o == "" || strings.ContainsAny(o, "?#") {
		return false
	}
	if !strings.HasPrefix(o, "http://") && !strings.HasPrefix(o, "https://") {
		return false
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(o, "https://"), "http://")
	return rest != "" && !strings.Contains(rest, "/")
}

// SourceConfigured reports whether the source has full configuration.
func (c *Config) SourceConfigured(s string) bool {
	switch s {
	case "aliyun":
		a := c.Aliyun
		return a.BaseURL != "" && a.Username != "" && a.PasswordFile != "" && a.Root != ""
	case "gdrive":
		return c.GDrive.Root != ""
	}
	return false
}

// JellyfinConfigured reports whether Jellyfin integration is configured.
func (c *Config) JellyfinConfigured() bool {
	return c.Jellyfin.BaseURL != "" && c.Jellyfin.APIKeyFile != "" && c.Jellyfin.LibraryID != ""
}

// ReadSecretFile reads a 0600 credential file. It refuses world/group
// readable files to fail closed on misconfigured deployments.
func ReadSecretFile(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("%s must not be group/world readable (mode %o)", path, fi.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimRight(string(b), "\n"), nil
}
