// Package config handles the on-disk, non-secret settings for t2b.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is the persisted, non-secret settings for the bot.
type Config struct {
	RPCEndpoint    string   `json:"rpc_endpoint"`
	PumpPortalKey  string   `json:"pumpportal_api_key,omitempty"` // empty = public feed only, no live trading
	PaperMode      bool     `json:"paper_mode"`
	StartingEquity float64  `json:"starting_equity"`
	WalletPubkey   string   `json:"wallet_pubkey"`
	FollowWallets  []string `json:"follow_wallets,omitempty"` // copy-trade these wallets' buys; manage with `t2b follow`
}

// Dir returns ~/.t2b, creating it if needed.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".t2b")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Exists reports whether a config file has already been written (init done).
func Exists() bool {
	p, err := path()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func Save(c *Config) error {
	p, err := path()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}
