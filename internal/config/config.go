package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}
func (d Duration) Value() time.Duration { return time.Duration(d) }

type Config struct {
	Version     int                   `yaml:"version"`
	Defaults    Defaults              `yaml:"defaults"`
	SSHProfiles map[string]SSHProfile `yaml:"ssh_profiles"`
	Connections []Connection          `yaml:"connections"`
}
type Defaults struct {
	Readonly       *bool    `yaml:"readonly"`
	ConnectTimeout Duration `yaml:"connect_timeout"`
	QueryTimeout   Duration `yaml:"query_timeout"`
	MaxRows        int      `yaml:"max_rows"`
}
type SSHProfile struct {
	Host                     string    `yaml:"host"`
	Port                     int       `yaml:"port"`
	Username                 string    `yaml:"username"`
	PrivateKeyFile           string    `yaml:"private_key_file"`
	PrivateKeyPassphraseFile string    `yaml:"private_key_passphrase_file"`
	KnownHostsFile           string    `yaml:"known_hosts_file"`
	HostKeyPolicy            string    `yaml:"host_key_policy"`
	KeepaliveInterval        Duration  `yaml:"keepalive_interval"`
	KeepaliveFailures        int       `yaml:"keepalive_failures"`
	Reconnect                Reconnect `yaml:"reconnect"`
}
type Reconnect struct {
	Enabled      bool     `yaml:"enabled"`
	InitialDelay Duration `yaml:"initial_delay"`
	MaxDelay     Duration `yaml:"max_delay"`
}
type Connection struct {
	ID             string   `yaml:"id"`
	Driver         string   `yaml:"driver"`
	Host           string   `yaml:"host"`
	Port           int      `yaml:"port"`
	Database       string   `yaml:"database"`
	Username       string   `yaml:"username"`
	PasswordFile   string   `yaml:"password_file"`
	SSHProfile     string   `yaml:"ssh_profile"`
	Readonly       *bool    `yaml:"readonly"`
	ConnectTimeout Duration `yaml:"connect_timeout"`
	QueryTimeout   Duration `yaml:"query_timeout"`
	MaxRows        int      `yaml:"max_rows"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err = yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	base := filepath.Dir(path)
	c.resolvePaths(base)
	return &c, c.Validate(base)
}

func (c *Config) resolvePaths(base string) {
	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}
	for name, p := range c.SSHProfiles {
		p.PrivateKeyFile = resolve(p.PrivateKeyFile)
		p.PrivateKeyPassphraseFile = resolve(p.PrivateKeyPassphraseFile)
		p.KnownHostsFile = resolve(p.KnownHostsFile)
		c.SSHProfiles[name] = p
	}
	for i := range c.Connections {
		c.Connections[i].PasswordFile = resolve(c.Connections[i].PasswordFile)
	}
}
func (c *Config) Validate(base string) error {
	if c.Version != 1 {
		return fmt.Errorf("version must be 1")
	}
	if len(c.Connections) == 0 {
		return fmt.Errorf("at least one connection is required")
	}
	for name, p := range c.SSHProfiles {
		if err := p.validate(base); err != nil {
			return fmt.Errorf("ssh_profiles.%s: %w", name, err)
		}
	}
	seen := map[string]bool{}
	for i, db := range c.Connections {
		if db.ID == "" || seen[db.ID] {
			return fmt.Errorf("connections[%d]: id must be unique and nonempty", i)
		}
		seen[db.ID] = true
		if db.Driver != "postgres" && db.Driver != "mysql" {
			return fmt.Errorf("connection %q: driver must be postgres or mysql", db.ID)
		}
		if db.Host == "" || db.Port < 1 || db.Port > 65535 {
			return fmt.Errorf("connection %q: valid host and port required", db.ID)
		}
		if db.Database == "" || db.Username == "" || db.PasswordFile == "" {
			return fmt.Errorf("connection %q: database, username, password_file required", db.ID)
		}
		if err := secureFile(db.PasswordFile); err != nil {
			return fmt.Errorf("connection %q: password_file: %w", db.ID, err)
		}
		if db.SSHProfile != "" {
			if _, ok := c.SSHProfiles[db.SSHProfile]; !ok {
				return fmt.Errorf("connection %q: unknown ssh_profile %q", db.ID, db.SSHProfile)
			}
		}
		if db.Readonly != nil && !*db.Readonly {
			return fmt.Errorf("connection %q: readonly=false is forbidden", db.ID)
		}
	}
	return nil
}
func (p SSHProfile) validate(base string) error {
	if p.Host == "" || p.Username == "" || p.PrivateKeyFile == "" {
		return fmt.Errorf("host, username, private_key_file required")
	}
	if p.Port == 0 {
		p.Port = 22
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("invalid port")
	}
	if p.HostKeyPolicy != "known_hosts" {
		return fmt.Errorf("host_key_policy must be known_hosts")
	}
	if p.KnownHostsFile == "" {
		return fmt.Errorf("known_hosts_file required")
	}
	for _, f := range []string{p.PrivateKeyFile, p.KnownHostsFile, p.PrivateKeyPassphraseFile} {
		if f != "" {
			if !filepath.IsAbs(f) {
				f = filepath.Join(base, f)
			}
			if _, err := os.Stat(f); err != nil {
				return fmt.Errorf("required file unavailable: %w", err)
			}
		}
	}
	if err := secureFile(p.PrivateKeyFile); err != nil {
		return fmt.Errorf("private_key_file: %w", err)
	}
	if p.PrivateKeyPassphraseFile != "" {
		if err := secureFile(p.PrivateKeyPassphraseFile); err != nil {
			return fmt.Errorf("private_key_passphrase_file: %w", err)
		}
	}
	return nil
}

func secureFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("permissions must not grant group or other access")
	}
	return nil
}
func (c Connection) Endpoint() string { return net.JoinHostPort(c.Host, fmt.Sprint(c.Port)) }
func ReadSecret(file string) (string, error) {
	b, e := os.ReadFile(file)
	if e != nil {
		return "", e
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}
