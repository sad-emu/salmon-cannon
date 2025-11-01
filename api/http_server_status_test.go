package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"salmoncannon/config"
	"salmoncannon/limiter"
	"salmoncannon/status"
)

func TestHandleStatus_ReturnsJSONList(t *testing.T) {
	cfg := &config.SalmonCannonConfig{
		Bridges: []config.SalmonBridgeConfig{
			{
				Name:                "bridge-one",
				TotalBandwidthLimit: config.SizeString(1024 * 1024), // 1MB/s = 8Mbps
			},
			{
				Name:                "bridge-two",
				TotalBandwidthLimit: config.SizeString(512 * 1024), // 512KB/s = 4Mbps
			},
		},
	}

	// Register mock limiters
	limiter1 := limiter.NewSharedLimiter(1024 * 1024)
	limiter2 := limiter.NewSharedLimiter(512 * 1024)
	status.GlobalConnMonitorRef.RegisterLimiter("bridge-one", limiter1)
	status.GlobalConnMonitorRef.RegisterLimiter("bridge-two", limiter2)

	srv := NewServer(cfg, ":0")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	w := httptest.NewRecorder()

	srv.handleStatus(w, req)

	res := w.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 got %d", res.StatusCode)
	}

	var list []statusDTO
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if got, want := len(list), 2; got != want {
		t.Fatalf("unexpected list length: got %d want %d", got, want)
	}

	// Check bridge-one
	if list[0].BridgeName != "bridge-one" {
		t.Fatalf("unexpected bridge name: %s", list[0].BridgeName)
	}
	expectedMaxBps := int64(1024 * 1024 * 8) // 8Mbps
	if list[0].MaxRateBitsPerSec != expectedMaxBps {
		t.Fatalf("unexpected max rate: got %d want %d", list[0].MaxRateBitsPerSec, expectedMaxBps)
	}

	// Check bridge-two
	if list[1].BridgeName != "bridge-two" {
		t.Fatalf("unexpected bridge name: %s", list[1].BridgeName)
	}
	expectedMaxBps2 := int64(512 * 1024 * 8) // 4Mbps
	if list[1].MaxRateBitsPerSec != expectedMaxBps2 {
		t.Fatalf("unexpected max rate: got %d want %d", list[1].MaxRateBitsPerSec, expectedMaxBps2)
	}
}

func TestHandleStatus_MethodNotAllowed(t *testing.T) {
	cfg := &config.SalmonCannonConfig{}
	srv := NewServer(cfg, ":0")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/status", nil)
	w := httptest.NewRecorder()

	srv.handleStatus(w, req)

	res := w.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405 got %d", res.StatusCode)
	}
}
