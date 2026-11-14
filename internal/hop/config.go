// Package hop is HopDrop: LAN discovery, the HTTP API, and the receive engine.
package hop

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// DefaultPort is the HTTP port; DiscoveryPort is the fixed UDP port for hellos.
	DefaultPort   = 47100
	DiscoveryPort = 47100
	Version       = "0.2.0"
)

// Config is this device's identity and settings, stored as config.json in ConfigDir.
type Config struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Port        int      `json:"port"`
	DownloadDir string   `json:"download_dir"`
	Peers       []string `json:"peers,omitempty"` // added by address (host:port)
}

// ConfigDir holds config.json and hop.log. HOPDROP_HOME overrides it.
func ConfigDir() string {
	if d := os.Getenv("HOPDROP_HOME"); d != "" {
		return d
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "hopdrop")
	}
	return filepath.Join(homeDir(), ".config", "hopdrop")
}

// CacheDir holds the spool for browser tabs' inboxes.
func CacheDir() string {
	if d := os.Getenv("HOPDROP_HOME"); d != "" {
		return filepath.Join(d, "cache")
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "hopdrop")
	}
	return filepath.Join(homeDir(), ".cache", "hopdrop")
}

func configPath() string { return filepath.Join(ConfigDir(), "config.json") }

// LoadConfig reads config.json, creating it with defaults on first run.
func LoadConfig() (Config, error) {
	var c Config
	data, err := os.ReadFile(configPath())
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &c); err != nil {
			return c, fmt.Errorf("parsing %s: %w", configPath(), err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return c, err
	}
	dirty := false
	if c.ID == "" {
		c.ID, dirty = newID(), true
	}
	if c.Name == "" {
		c.Name, dirty = defaultName(), true
	}
	if c.Port == 0 {
		c.Port, dirty = DefaultPort, true
	}
	if c.DownloadDir == "" {
		c.DownloadDir, dirty = filepath.Join(homeDir(), "Downloads", "HopDrop"), true
	}
	if dirty {
		return c, c.Save()
	}
	return c, nil
}

// Save writes config.json atomically.
func (c Config) Save() error {
	if err := os.MkdirAll(ConfigDir(), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	tmp := configPath() + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, configPath())
}

// DeviceType is mac, pi, linux, win or other.
func DeviceType() string {
	switch runtime.GOOS {
	case "darwin":
		return "mac"
	case "windows":
		return "win"
	case "linux":
		if model, _ := os.ReadFile("/proc/device-tree/model"); strings.Contains(string(model), "Raspberry Pi") {
			return "pi"
		}
		return "linux"
	}
	return "other"
}

func defaultName() string {
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil {
			if name := strings.TrimSpace(string(out)); name != "" {
				return name
			}
		}
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return strings.TrimSuffix(h, ".local")
	}
	return "hopdrop-" + DeviceType()
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

// newID returns 32 random hex characters.
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
