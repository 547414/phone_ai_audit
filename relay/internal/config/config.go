package config

import (
	"bufio"
	"errors"
	"os"
	"strings"
)

type Config struct {
	ListenAddr    string
	DBPath        string
	MacToken      string
	AppToken      string
	PublicBaseURL string
}

func Load() (Config, error) {
	loadDotEnvIfPresent()

	cfg := Config{
		ListenAddr:    envOr("LISTEN_ADDR", "127.0.0.1:8787"),
		DBPath:        envOr("DB_PATH", "./data/audit.db"),
		MacToken:      strings.TrimSpace(os.Getenv("MAC_TOKEN")),
		AppToken:      strings.TrimSpace(os.Getenv("APP_TOKEN")),
		PublicBaseURL: strings.TrimRight(envOr("PUBLIC_BASE_URL", ""), "/"),
	}
	if cfg.MacToken == "" || cfg.AppToken == "" {
		return cfg, errors.New("MAC_TOKEN and APP_TOKEN must both be set")
	}
	if cfg.MacToken == cfg.AppToken {
		return cfg, errors.New("MAC_TOKEN and APP_TOKEN must be distinct")
	}
	return cfg, nil
}

// loadDotEnvIfPresent loads key=value pairs from RELAY_ENV_FILE or ./.env when present,
// without overriding variables already set in the process environment.
func loadDotEnvIfPresent() {
	path := strings.TrimSpace(os.Getenv("RELAY_ENV_FILE"))
	if path == "" {
		path = ".env"
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if len(val) >= 2 && ((val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'')) {
			val = val[1 : len(val)-1]
		}
		if key != "" && os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
		}
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
