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
		{"10KB", 1024 * 10, false},
		{"10K", 1000 * 10 / 8, false},
		{"2MB", 2 << 20, false},
		{"1GB", 1 << 30, false},
		{"100", 100, false},
		{"bad", 0, true},
		{"10k", 0, true}, // lowercase not allowed
		{"50MB", 52428800, false},
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
	if b.TotalBandwidthLimit != -1 {
		t.Errorf("TotalBandwidthLimit default not set")
	}
	if b.InterfaceName != "" {
		t.Errorf("InterfaceName default not set")
	}
	if b.MaxRecieveBufferSize != SizeString(419430400) {
		t.Errorf("MaxRecieveBufferSize default not set to expected value, got %d", b.MaxRecieveBufferSize)
	}
}

func TestLoadConfig(t *testing.T) {
	yamlData := `SalmonBridges:
  - SBName: test
    SBSocksListenPort: 1080
    SBConnect: true
    SBFarPort: 1100
    SBFarIp: "127.0.0.1"
    SBIdleTimeout: "15s"
    SBInitialPacketSize: 1500
    SBRecieveWindow: "20M"
    SBMaxRecieveWindow: "50M"
    SBTotalBandwidthLimit: "200M"
    SBInterfaceName: "eth0"
    SBMaxRecieveBufferSize: 500MB
    SBAllowedInAddresses:
      - "127.0.0.1"
      - "127.0.0.2"
    SBAllowedOutAddresses:
      - "example.com"
      - "example2.com"
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
	if b.Name != "test" || b.SocksListenPort != 1080 || b.Connect != true || b.FarPort != 1100 || b.FarIp != "127.0.0.1" {
		t.Errorf("bridge fields not parsed correctly: %+v", b)
	}
	if b.IdleTimeout != DurationString(15*time.Second) {
		t.Errorf("IdleTimeout not parsed correctly")
	}
	if b.InitialPacketSize != 1500 {
		t.Errorf("InitialPacketSize not parsed correctly")
	}
	if b.TotalBandwidthLimit != SizeString(25000000) {
		t.Errorf("TotalBandwidthLimit not parsed correctlygot %d", b.TotalBandwidthLimit)
	}
	if b.InterfaceName != "eth0" {
		t.Errorf("InterfaceName not parsed correctly, got %q", b.InterfaceName)
	}
	if b.MaxRecieveBufferSize != 524288000 {
		t.Errorf("MaxRecieveBufferSize not parsed correctly, got %d", b.MaxRecieveBufferSize)
	}
	if len(b.AllowedInAddresses) != 2 {
		t.Errorf("AllowedInAddresses not parsed correctly, got %d", len(b.AllowedInAddresses))
	}
	if len(b.AllowedOutAddresses) != 2 {
		t.Errorf("AllowedOutAddresses not parsed correctly, got %d", len(b.AllowedOutAddresses))
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
	yamlData := `GlobalLog:
  Filename: "custom.log"
  MaxSize: 42
  MaxBackups: 7
  MaxAge: 99
  Compress: true
SalmonBridges:
  - SBName: test
    SBSocksListenPort: 1080
    SBConnect: true
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
	if cfg.Bridges[0].InterfaceName != "" {
		t.Errorf("InterfaceName not parsed correctly, got %v", cfg.Bridges[0].InterfaceName)
	}
}

func TestApiConfig_ParseYAML(t *testing.T) {
	yamlData := `ApiConfig:
  Hostname: "localhost"
  Port: 8080
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
	if cfg.ApiConfig == nil {
		t.Fatalf("ApiConfig should not be nil after parsing YAML")
	}
	if cfg.ApiConfig.Hostname != "localhost" {
		t.Errorf("Hostname not parsed correctly, got %q", cfg.ApiConfig.Hostname)
	}
	if cfg.ApiConfig.Port != 8080 {
		t.Errorf("Port not parsed correctly, got %d", cfg.ApiConfig.Port)
	}
}

func TestSocksRedirectConfig_ParseYAML(t *testing.T) {
	yamlData := `SocksRedirect:
  Hostname: "localhost"
  Port: 8082
  Redirects:
    "example.com": "bridge-one"
    "example.org": "bridge-two"
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
	if cfg.SocksRedirectConfig == nil {
		t.Fatalf("SocksRedirectConfig should not be nil after parsing YAML")
	}
	if cfg.SocksRedirectConfig.Hostname != "localhost" {
		t.Errorf("Hostname not parsed correctly, got %q", cfg.SocksRedirectConfig.Hostname)
	}
	if cfg.SocksRedirectConfig.Port != 8082 {
		t.Errorf("Port not parsed correctly, got %d", cfg.SocksRedirectConfig.Port)
	}
	if len(cfg.SocksRedirectConfig.Redirects) != 2 {
		t.Errorf("Redirects not parsed correctly, got %d", len(cfg.SocksRedirectConfig.Redirects))
	}
	if cfg.SocksRedirectConfig.Redirects["example.com"] != "bridge-one" {
		t.Errorf("Redirect for example.com not parsed correctly, got %q", cfg.SocksRedirectConfig.Redirects["example.com"])
	}
	if cfg.SocksRedirectConfig.Redirects["example.org"] != "bridge-two" {
		t.Errorf("Redirect for example.org not parsed correctly, got %q", cfg.SocksRedirectConfig.Redirects["example.org"])
	}
}

func TestSalmonBounceConfig_ParseYAML(t *testing.T) {
	yamlData := `SalmonBounces:
  - SBName: "bounce-one"
    SBListenAddr: "0.0.0.0:8080"
    SBRouteMap:
      "192.168.1.1": "backend1:9090"
      "192.168.1.2": "backend2:9091"
    SBIdleTimeout: "30s"
  - SBName: "bounce-two"
    SBListenAddr: ":8081"
    SBRouteMap:
      "10.0.0.1": "backend3:9092"
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
	if len(cfg.Bounces) != 2 {
		t.Fatalf("expected 2 bounces, got %d", len(cfg.Bounces))
	}

	// Check first bounce
	b1 := cfg.Bounces[0]
	if b1.Name != "bounce-one" {
		t.Errorf("bounce 1: expected name 'bounce-one', got %q", b1.Name)
	}
	if b1.ListenAddr != "0.0.0.0:8080" {
		t.Errorf("bounce 1: expected ListenAddr '0.0.0.0:8080', got %q", b1.ListenAddr)
	}
	if len(b1.RouteMap) != 2 {
		t.Errorf("bounce 1: expected 2 routes, got %d", len(b1.RouteMap))
	}
	if b1.RouteMap["192.168.1.1"] != "backend1:9090" {
		t.Errorf("bounce 1: route for 192.168.1.1 not parsed correctly, got %q", b1.RouteMap["192.168.1.1"])
	}
	if b1.IdleTimeout != DurationString(30*time.Second) {
		t.Errorf("bounce 1: expected IdleTimeout 30s, got %v", b1.IdleTimeout)
	}

	// Check second bounce (should have default idle timeout)
	b2 := cfg.Bounces[1]
	if b2.Name != "bounce-two" {
		t.Errorf("bounce 2: expected name 'bounce-two', got %q", b2.Name)
	}
	if b2.ListenAddr != ":8081" {
		t.Errorf("bounce 2: expected ListenAddr ':8081', got %q", b2.ListenAddr)
	}
	if len(b2.RouteMap) != 1 {
		t.Errorf("bounce 2: expected 1 route, got %d", len(b2.RouteMap))
	}
	if b2.IdleTimeout != DurationString(60*time.Second) {
		t.Errorf("bounce 2: expected default IdleTimeout 60s, got %v", b2.IdleTimeout)
	}
}
