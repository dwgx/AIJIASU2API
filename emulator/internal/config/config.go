// Package config loads the emulator's JSON configuration.
//
// The file holds the API host list, account credentials, transport selection,
// listen addresses, and optional device overrides. It must never be committed
// with real credentials.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"

	"ajsproxy/internal/api"
)

// DefaultHosts is the API fallback list embedded in the shipping clients and
// in product.dat (ServerApiUrls).
var DefaultHosts = []string{
	"https://login.ajs-app.com",
	"https://login.mmegg.com",
	"https://login.mmegg.com:4443",
	"https://login.mbclubsh.com",
}

// Account holds the control API credentials.
type Account struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Listen holds the local proxy listen addresses.
type Listen struct {
	SOCKS5 string `json:"socks5"`
	HTTP   string `json:"http"`
}

// Timeouts holds optional timing overrides in the units named by the field.
type Timeouts struct {
	HandshakeSeconds int `json:"handshake_seconds,omitempty"`
	IdleMilliseconds int `json:"idle_milliseconds,omitempty"`
}

// Config is the JSON configuration file.
type Config struct {
	Hosts       []string          `json:"hosts"`
	Account     Account           `json:"account"`
	Transport   string            `json:"transport"`
	Listen      Listen            `json:"listen"`
	Device      map[string]string `json:"device,omitempty"`
	CoreVersion string            `json:"core_version,omitempty"`
	OAEPHash    string            `json:"oaep_hash,omitempty"`
	Server      string            `json:"server,omitempty"`
	Timeouts    Timeouts          `json:"timeouts,omitempty"`
}

// Default returns the built-in configuration.
func Default() *Config {
	return &Config{
		Hosts:     append([]string(nil), DefaultHosts...),
		Transport: "rawtcp",
		Listen: Listen{
			SOCKS5: "127.0.0.1:1080",
			HTTP:   "127.0.0.1:8080",
		},
		CoreVersion: "20260525-1",
	}
}

// Load reads and validates a JSON config file. Missing optional fields take
// their defaults; the file is not created.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if len(cfg.Hosts) == 0 {
		cfg.Hosts = append([]string(nil), DefaultHosts...)
	}
	if cfg.Transport == "" {
		cfg.Transport = "rawtcp"
	}
	if cfg.Listen.SOCKS5 == "" {
		cfg.Listen.SOCKS5 = "127.0.0.1:1080"
	}
	if cfg.Listen.HTTP == "" {
		cfg.Listen.HTTP = "127.0.0.1:8080"
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks the fields the emulator cannot default.
func (c *Config) Validate() error {
	if c.Transport != "rawtcp" && c.Transport != "httpupgrade" {
		return fmt.Errorf("config: unknown transport %q (want rawtcp or httpupgrade)", c.Transport)
	}
	if c.Timeouts.HandshakeSeconds < 0 {
		return errors.New("config: handshake_seconds must be >= 0")
	}
	if c.Timeouts.IdleMilliseconds < 0 {
		return errors.New("config: idle_milliseconds must be >= 0")
	}
	return nil
}

// ApplyDevice applies the optional device overrides to d. Recognised keys are
// the control API field names: _site, _v, _m, _os_ver, _os_dev, _os_hw,
// _os_lang, _os_dim, _inst_mkt, _lang, _vrf, _tz.
func (c *Config) ApplyDevice(d *api.Device) error {
	for key, value := range c.Device {
		switch key {
		case "_site":
			d.Site = value
		case "_v":
			d.Version = value
		case "_m":
			d.Platform = value
		case "_os_ver":
			d.OSVersion = value
		case "_os_dev":
			d.OSDevice = value
		case "_os_hw":
			d.OSHW = value
		case "_os_lang":
			d.OSLang = value
		case "_os_dim":
			d.OSDim = value
		case "_inst_mkt":
			d.InstMkt = value
		case "_lang":
			d.Lang = value
		case "_vrf":
			d.Vrf = value
		case "_tz":
			tz, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("config: device._tz %q is not an integer", value)
			}
			d.TZMinutes = tz
		default:
			return fmt.Errorf("config: unknown device key %q", key)
		}
	}
	return nil
}
