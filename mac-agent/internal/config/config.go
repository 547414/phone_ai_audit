package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	ServerURL  string   `json:"server_url"`
	MacToken   string   `json:"mac_token"`
	AppToken   string   `json:"app_token,omitempty"`
	SocketPath string   `json:"socket_path"`
	Hostname   string   `json:"hostname,omitempty"`
	FileRoots  []string `json:"file_roots,omitempty"`
}

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ai-audit", "config.json")
}

func DefaultSocketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "run", "ai-audit.sock")
}

func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func Load() (Config, error) {
	path := os.Getenv("AI_AUDIT_CONFIG")
	if path == "" {
		path = DefaultPath()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.ServerURL = strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/")
	cfg.SocketPath = expandHome(cfg.SocketPath)
	if cfg.SocketPath == "" {
		cfg.SocketPath = DefaultSocketPath()
	}
	if cfg.Hostname == "" {
		cfg.Hostname, _ = os.Hostname()
	}
	if len(cfg.FileRoots) == 0 {
		if envRoots := strings.TrimSpace(os.Getenv("AI_AUDIT_FILE_ROOTS")); envRoots != "" {
			for _, part := range strings.Split(envRoots, ",") {
				if r := expandHome(part); r != "" {
					cfg.FileRoots = append(cfg.FileRoots, r)
				}
			}
		}
	}
	if len(cfg.FileRoots) == 0 {
		home, _ := os.UserHomeDir()
		cfg.FileRoots = []string{home}
	}
	return cfg, nil
}

func Save(cfg Config) error {
	path := os.Getenv("AI_AUDIT_CONFIG")
	if path == "" {
		path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func (c Config) WSBaseURL() string {
	if strings.HasPrefix(c.ServerURL, "https://") {
		return "wss://" + strings.TrimPrefix(c.ServerURL, "https://")
	}
	if strings.HasPrefix(c.ServerURL, "http://") {
		return "ws://" + strings.TrimPrefix(c.ServerURL, "http://")
	}
	return "wss://" + c.ServerURL
}
