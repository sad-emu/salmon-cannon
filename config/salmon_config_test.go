package config

import (
	"os"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDurationString_UnmarshalYAML(t *testing.T) {
	var d DurationString
	cases := []struct {
		input     string
		expect    time.Duration
		shouldErr bool
	}{
		{"10s", 10 * time.Second, false},
		{"5m", 5 * time.Minute, false},
		{"15", 15 * time.Second, false}, // int tag
		{"bad", 0, true},
		{"10h", 0, true},
	}
	for _, c := range cases {
		var node yaml.Node
		node.Value = c.input
		if c.input == "15" {
			node.Tag = "!!int"
		}
		err := d.UnmarshalYAML(&node)
		if c.shouldErr && err == nil {
			t.Errorf("expected error for input %q", c.input)
		}
		if !c.shouldErr && (err != nil || time.Duration(d) != c.expect) {
			t.Errorf("input %q: got %v, want %v", c.input, time.Duration(d), c.expect)
		}
	}
}

func TestSizeString_UnmarshalYAML(t *testing.T) {
	var s SizeString
	cases := []struct {
		input     string
		expect    int64
		shouldErr bool
	}{
		{"10K", 10 << 10, false},
		{"2M", 2 << 20, false},
		{"1G", 1 << 30, false},
		{"100", 100, false},
		{"bad", 0, true},
		{"10k", 0, true}, // lowercase not allowed
	}
	for _, c := range cases {
		var node yaml.Node
		node.Value = c.input
		err := s.UnmarshalYAML(&node)
		if c.shouldErr && err == nil {
			t.Errorf("expected error for input %q", c.input)
		}
		if !c.shouldErr && (err != nil || int64(s) != c.expect) {
			t.Errorf("input %q: got %v, want %v", c.input, int64(s), c.expect)
		}
	}
}

func TestSetDefaults(t *testing.T) {
	cfg := SalmonCannonConfig{
		Bridges: []SalmonBridgeConfig{{}},
	}
	cfg.SetDefaults()
	b := cfg.Bridges[0]
	if b.IdleTimeout != DurationString(10*time.Second) {
		t.Errorf("IdleTimeout default not set")
	}
	if b.InitialPacketSize != 1350 {
		t.Errorf("InitialPacketSize default not set")
	}
	if b.RecieveWindow != SizeString(175<<20) {
		t.Errorf("RecieveWindow default not set")
	}
	if b.MaxRecieveWindow != SizeString(400<<20) {
		t.Errorf("MaxRecieveWindow default not set")
	}
	if b.TotalBandwidthLimit != -1 {
		t.Errorf("TotalBandwidthLimit default not set")
	}
}

func TestLoadConfig(t *testing.T) {
	yamlData := `salmonbridges:
  - SBName: test
    SBSocksListenPort: 1080
    SBConnect: true
    SBNearPort: 1099
    SBFarPort: 1100
    SBFarIp: "127.0.0.1"
    SBIdleTimeout: "15s"
    SBInitialPacketSize: 1500
    SBRecieveWindow: "20M"
    SBMaxRecieveWindow: "50M"
    SBTotalBandwidthLimit: "200M"
`
	f, err := os.CreateTemp("", "salmon_config_test.yaml")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	f.WriteString(yamlData)
	f.Close()

	cfg, err := LoadConfig(f.Name())
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if len(cfg.Bridges) != 1 {
		t.Errorf("expected 1 bridge, got %d", len(cfg.Bridges))
	}
	b := cfg.Bridges[0]
	if b.Name != "test" || b.SocksListenPort != 1080 || b.Connect != true || b.NearPort != 1099 || b.FarPort != 1100 || b.FarIp != "127.0.0.1" {
		t.Errorf("bridge fields not parsed correctly: %+v", b)
	}
	if b.IdleTimeout != DurationString(15*time.Second) {
		t.Errorf("IdleTimeout not parsed correctly")
	}
	if b.InitialPacketSize != 1500 {
		t.Errorf("InitialPacketSize not parsed correctly")
	}
	if b.RecieveWindow != SizeString(20<<20) {
		t.Errorf("RecieveWindow not parsed correctly")
	}
	if b.MaxRecieveWindow != SizeString(50<<20) {
		t.Errorf("MaxRecieveWindow not parsed correctly")
	}
	if b.TotalBandwidthLimit != SizeString(200<<20) {
		t.Errorf("TotalBandwidthLimit not parsed correctly")
	}
}

func TestGlobalLogConfig_Defaults(t *testing.T) {
	cfg := SalmonCannonConfig{}
	cfg.SetDefaults()
	if cfg.GlobalLog == nil {
		t.Fatalf("GlobalLog should not be nil after SetDefaults")
	}
	if cfg.GlobalLog.Filename != "" {
		t.Errorf("Filename default should not be set, got %q", cfg.GlobalLog.Filename)
	}
	if cfg.GlobalLog.MaxSize != 1 {
		t.Errorf("MaxSize default not set, got %d", cfg.GlobalLog.MaxSize)
	}
	if cfg.GlobalLog.MaxBackups != 1 {
		t.Errorf("MaxBackups default not set, got %d", cfg.GlobalLog.MaxBackups)
	}
	if cfg.GlobalLog.MaxAge != 1 {
		t.Errorf("MaxAge default not set, got %d", cfg.GlobalLog.MaxAge)
	}
	if cfg.GlobalLog.Compress != false {
		t.Errorf("Compress default not set, got %v", cfg.GlobalLog.Compress)
	}
}

func TestGlobalLogConfig_ParseYAML(t *testing.T) {
	yamlData := `globallog:
  Filename: "custom.log"
  MaxSize: 42
  MaxBackups: 7
  MaxAge: 99
  Compress: true
salmonbridges:
  - SBName: test
    SBSocksListenPort: 1080
    SBConnect: true
    SBNearPort: 1099
    SBFarPort: 1100
    SBFarIp: "127.0.0.1"
    SBIdleTimeout: "15s"
    SBInitialPacketSize: 1500
    SBRecieveWindow: "20M"
    SBMaxRecieveWindow: "50M"
    SBTotalBandwidthLimit: "200M"
`
	f, err := os.CreateTemp("", "salmon_config_test.yaml")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(f.Name())
	f.WriteString(yamlData)
	f.Close()

	cfg, err := LoadConfig(f.Name())
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.GlobalLog == nil {
		t.Fatalf("GlobalLog should not be nil after parsing YAML")
	}
	if cfg.GlobalLog.Filename != "custom.log" {
		t.Errorf("Filename not parsed correctly, got %q", cfg.GlobalLog.Filename)
	}
	if cfg.GlobalLog.MaxSize != 42 {
		t.Errorf("MaxSize not parsed correctly, got %d", cfg.GlobalLog.MaxSize)
	}
	if cfg.GlobalLog.MaxBackups != 7 {
		t.Errorf("MaxBackups not parsed correctly, got %d", cfg.GlobalLog.MaxBackups)
	}
	if cfg.GlobalLog.MaxAge != 99 {
		t.Errorf("MaxAge not parsed correctly, got %d", cfg.GlobalLog.MaxAge)
	}
	if cfg.GlobalLog.Compress != true {
		t.Errorf("Compress not parsed correctly, got %v", cfg.GlobalLog.Compress)
	}
}
