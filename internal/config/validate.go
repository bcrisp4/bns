package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"time"
)

var validLogLevels = []string{"debug", "info", "warn", "error"}
var validLogFormats = []string{"json", "text"}

// Validate returns nil if the config is internally consistent and usable.
func (c Config) Validate() error {
	if err := validateBind(c.Listen.UDP, "listen.udp"); err != nil {
		return err
	}
	if err := validateBind(c.Listen.TCP, "listen.tcp"); err != nil {
		return err
	}
	if err := validateBind(c.Admin.Listen, "admin.listen"); err != nil {
		return err
	}
	if c.Listen.QueryTimeout <= 0 {
		return errors.New("listen.query_timeout must be > 0")
	}
	if len(c.Upstreams) == 0 {
		return errors.New("at least one upstream is required")
	}
	for i, u := range c.Upstreams {
		if _, _, err := net.SplitHostPort(u.Addr); err != nil {
			return fmt.Errorf("upstreams[%d].addr %q: %w", i, u.Addr, err)
		}
		if u.Timeout <= 0 {
			return fmt.Errorf("upstreams[%d].timeout must be > 0", i)
		}
	}
	if c.Cache.Capacity <= 0 {
		return errors.New("cache.capacity must be > 0")
	}
	if c.Cache.MaxTTL <= 0 {
		return errors.New("cache.max_ttl must be > 0")
	}
	if c.Cache.NegativeTTLMax <= 0 {
		return errors.New("cache.negative_ttl_max must be > 0")
	}
	if !slices.Contains(validLogLevels, c.Logging.Level) {
		return fmt.Errorf("logging.level %q must be one of %v", c.Logging.Level, validLogLevels)
	}
	if !slices.Contains(validLogFormats, c.Logging.Format) {
		return fmt.Errorf("logging.format %q must be one of %v", c.Logging.Format, validLogFormats)
	}
	if c.ShutdownTimeout <= 0 {
		return errors.New("shutdown_timeout must be > 0")
	}
	if c.StartupProbeTimeout <= 0 {
		return errors.New("startup_probe_timeout must be > 0")
	}
	if c.Blocklists.RefreshInterval > 0 && c.Blocklists.RefreshInterval < time.Minute {
		return fmt.Errorf("blocklists.refresh_interval %s must be >= 1m (or 0 to disable refresh)", c.Blocklists.RefreshInterval)
	}
	seenNames := make(map[string]struct{}, len(c.Blocklists.Sources))
	for i, s := range c.Blocklists.Sources {
		if s.Name == "" {
			return fmt.Errorf("blocklists.sources[%d].name is required", i)
		}
		if _, dup := seenNames[s.Name]; dup {
			return fmt.Errorf("blocklists.sources[%d].name %q must be unique", i, s.Name)
		}
		seenNames[s.Name] = struct{}{}
		switch s.Type {
		case "file":
			if s.Path == "" {
				return fmt.Errorf("blocklists.sources[%d] (%s): path is required for type=file", i, s.Name)
			}
		case "http":
			if s.URL == "" {
				return fmt.Errorf("blocklists.sources[%d] (%s): url is required for type=http", i, s.Name)
			}
			u, err := url.Parse(s.URL)
			if err != nil {
				return fmt.Errorf("blocklists.sources[%d] (%s): url %q: %w", i, s.Name, s.URL, err)
			}
			if u.Scheme != "http" && u.Scheme != "https" {
				return fmt.Errorf("blocklists.sources[%d] (%s): url scheme %q must be http or https", i, s.Name, u.Scheme)
			}
		default:
			return fmt.Errorf("blocklists.sources[%d] (%s): type %q must be file or http", i, s.Name, s.Type)
		}
	}
	return nil
}

func validateBind(addr, field string) error {
	if addr == "" {
		return fmt.Errorf("%s is required", field)
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s %q: %w", field, addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return fmt.Errorf("%s %q: port must be a number in range 0-65535", field, addr)
	}
	return nil
}
