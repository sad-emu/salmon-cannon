package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// GlobalLogConfig holds optional global log file settings
type GlobalLogConfig struct {
	Filename   string `yaml:"Filename,omitempty"`
	MaxSize    int    `yaml:"MaxSize,omitempty"` // megabytes
	MaxBackups int    `yaml:"MaxBackups,omitempty"`
	MaxAge     int    `yaml:"MaxAge,omitempty"` // days
	Compress   bool   `yaml:"Compress,omitempty"`
}

// DurationString supports "10s", "5m" (only lowercase s/m)
type DurationString time.Duration

func (d *DurationString) UnmarshalYAML(value *yaml.Node) error {
	s := value.Value
	if value.Tag == "!!int" {
		v, err := strconv.Atoi(s)
		if err != nil {
			return err
		}
		*d = DurationString(time.Duration(v) * time.Second)
		return nil
	}
	if !(strings.HasSuffix(s, "s") || strings.HasSuffix(s, "m")) {
		return fmt.Errorf("invalid duration: %s (must end with 's' or 'm')", s)
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = DurationString(dur)
	return nil
}

func (d DurationString) Duration() time.Duration {
	return time.Duration(d)
}

// SizeString supports "10K", "10M", "1G" (uppercase only)
type SizeString int64

func (s *SizeString) UnmarshalYAML(value *yaml.Node) error {
	raw := value.Value
	if value.Tag == "!!int" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return err
		}
		*s = SizeString(v)
		return nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("empty size string")
	}
	multiplier := int64(1)
	switch {
	case strings.HasSuffix(raw, "K"):
		multiplier = 1000 / 8
		raw = strings.TrimSuffix(raw, "K")
	case strings.HasSuffix(raw, "KB"):
		multiplier = 1024
		raw = strings.TrimSuffix(raw, "KB")
	case strings.HasSuffix(raw, "M"):
		multiplier = (1000 * 1000) / 8
		raw = strings.TrimSuffix(raw, "M")
	case strings.HasSuffix(raw, "MB"):
		multiplier = 1024 * 1024
		raw = strings.TrimSuffix(raw, "MB")
	case strings.HasSuffix(raw, "G"):
		multiplier = (1000 * 1000 * 1000) / 8
		raw = strings.TrimSuffix(raw, "G")
	case strings.HasSuffix(raw, "GB"):
		multiplier = 1024 * 1024 * 1024
		raw = strings.TrimSuffix(raw, "GB")
	default:
		// Only accept numbers or uppercase suffix
		if _, err := strconv.ParseInt(raw, 10, 64); err != nil {
			return fmt.Errorf("invalid size string: %s (must end with 'K','M','G')", value.Value)
		}
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return err
	}
	*s = SizeString(v * multiplier)
	return nil
}

// SalmonBridgeConfig holds config for one bridge instance
type SalmonBridgeConfig struct {
	Name            string `yaml:"SBName"`
	SocksListenPort int    `yaml:"SBSocksListenPort"`
	Connect         bool   `yaml:"SBConnect"`
	NearPort        int    `yaml:"SBNearPort,omitempty"`
	FarPort         int    `yaml:"SBFarPort,omitempty"`
	FarIp           string `yaml:"SBFarIp"`

	SocksListenAddress   string         `yaml:"SBSocksListenAddress,omitempty"`   // e.g. "127.0.0.1"
	HttpListenPort       int            `yaml:"SBHttpListenPort,omitempty"`       // optional HTTP proxy listen port (near only)
	IdleTimeout          DurationString `yaml:"SBIdleTimeout,omitempty"`          // default "10s"
	InitialPacketSize    int            `yaml:"SBInitialPacketSize,omitempty"`    // default 1350
	TotalBandwidthLimit  SizeString     `yaml:"SBTotalBandwidthLimit,omitempty"`  // default "100M"
	MaxRecieveBufferSize SizeString     `yaml:"SBMaxRecieveBufferSize,omitempty"` // default "500MB"
	InterfaceName        string         `yaml:"SBInterfaceName,omitempty"`        // default ""
	AllowedInAddresses   []string       `yaml:"SBAllowedInAddresses,omitempty"`   // default []
	AllowedOutAddresses  []string       `yaml:"SBAllowedOutAddresses,omitempty"`  // default []
}

// Config holds all SalmonBridgeConfigs
type SalmonCannonConfig struct {
	Bridges   []SalmonBridgeConfig `yaml:"SalmonBridges"`
	GlobalLog *GlobalLogConfig     `yaml:"GlobalLog,omitempty"`
}

// SetDefaults sets default values for optional fields
func (c *SalmonCannonConfig) SetDefaults() {
	for i, b := range c.Bridges {
		if len(b.SocksListenAddress) == 0 {
			c.Bridges[i].SocksListenAddress = "127.0.0.1"
		}

		// These values are never used for these types
		if b.Connect == true {
			if b.NearPort == 0 {
				c.Bridges[i].NearPort = b.FarPort
			}
		} else {
			if b.FarPort == 0 {
				c.Bridges[i].FarPort = b.NearPort
			}
		}

		if b.IdleTimeout == 0 {
			c.Bridges[i].IdleTimeout = DurationString(10 * time.Second)
		}
		if b.InitialPacketSize == 0 {
			c.Bridges[i].InitialPacketSize = 1350
		}
		if b.TotalBandwidthLimit == 0 {
			c.Bridges[i].TotalBandwidthLimit = -1
		} else {
			c.Bridges[i].TotalBandwidthLimit = b.TotalBandwidthLimit
		}
		if len(b.InterfaceName) == 0 {
			c.Bridges[i].InterfaceName = ""
		}
		if b.MaxRecieveBufferSize == 0 {
			c.Bridges[i].MaxRecieveBufferSize = SizeString(419430400) // 400MB
		} else if b.MaxRecieveBufferSize <= 1024*1024*7 {
			fmt.Errorf("MaxBufferSize is too low. Cannot be below 7MB.")
		}
	}
	// Set global log defaults if not provided
	if c.GlobalLog == nil {
		c.GlobalLog = &GlobalLogConfig{
			Filename:   "", // Empty string means log to stdout
			MaxSize:    1,
			MaxBackups: 1,
			MaxAge:     1,
			Compress:   false,
		}
	} else {
		if c.GlobalLog.Filename == "" {
			c.GlobalLog.Filename = "sc.log"
		}
		if c.GlobalLog.MaxSize == 0 {
			c.GlobalLog.MaxSize = 20
		}
		if c.GlobalLog.MaxBackups == 0 {
			c.GlobalLog.MaxBackups = 5
		}
		if c.GlobalLog.MaxAge == 0 {
			c.GlobalLog.MaxAge = 28
		}
		// Compress defaults to false, so no need to set
	}
}

// LoadConfig loads config from YAML file and parses it
func LoadConfig(path string) (*SalmonCannonConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg SalmonCannonConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	cfg.SetDefaults()
	return &cfg, nil
}
