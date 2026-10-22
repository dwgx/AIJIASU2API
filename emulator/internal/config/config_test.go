package config

import (
	"os"
	"path/filepath"
	"testing"

	"ajsproxy/internal/api"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
	  "hosts": ["https://example.invalid"],
	  "account": {"username": "u", "password": "p"},
	  "transport": "httpupgrade",
	  "listen": {"socks5": "127.0.0.1:19080", "http": "127.0.0.1:19081"},
	  "device": {"_v": "4.9.6.0", "_tz": "-300"},
	  "core_version": "20250708-1",
	  "server": "s1",
	  "timeouts": {"handshake_seconds": 5, "idle_milliseconds": 250}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Hosts) != 1 || cfg.Hosts[0] != "https://example.invalid" {
		t.Fatalf("hosts = %v", cfg.Hosts)
	}
	if cfg.Account.Username != "u" || cfg.Account.Password != "p" {
		t.Fatalf("account = %+v", cfg.Account)
	}
	if cfg.Transport != "httpupgrade" || cfg.Server != "s1" {
		t.Fatalf("transport/server = %q/%q", cfg.Transport, cfg.Server)
	}
	if cfg.Timeouts.HandshakeSeconds != 5 || cfg.Timeouts.IdleMilliseconds != 250 {
		t.Fatalf("timeouts = %+v", cfg.Timeouts)
	}

	device := api.DefaultDevice()
	if err := cfg.ApplyDevice(&device); err != nil {
		t.Fatalf("ApplyDevice: %v", err)
	}
	if device.Version != "4.9.6.0" || device.TZMinutes != -300 {
		t.Fatalf("device overrides = %+v", device)
	}
	if device.Site != "ajs" || device.Platform != "android" {
		t.Fatalf("untouched device fields changed: %+v", device)
	}

	cfg.Device = map[string]string{"_nope": "x"}
	if err := cfg.ApplyDevice(&device); err == nil {
		t.Fatal("unknown device key was accepted")
	}

	cfg.Transport = "carrier-pigeon"
	if err := cfg.Validate(); err == nil {
		t.Fatal("unknown transport was accepted")
	}

	// Missing optional fields take defaults.
	path2 := filepath.Join(dir, "minimal.json")
	if err := os.WriteFile(path2, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg2, err := Load(path2)
	if err != nil {
		t.Fatalf("Load minimal: %v", err)
	}
	if len(cfg2.Hosts) != len(DefaultHosts) || cfg2.Transport != "rawtcp" {
		t.Fatalf("defaults not applied: %+v", cfg2)
	}
	if cfg2.Listen.SOCKS5 != "127.0.0.1:1080" || cfg2.Listen.HTTP != "127.0.0.1:8080" {
		t.Fatalf("listen defaults not applied: %+v", cfg2.Listen)
	}
}
