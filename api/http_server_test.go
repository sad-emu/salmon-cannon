package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"salmoncannon/config"
)

func TestHandleBridges_ReturnsJSONList(t *testing.T) {
	cfg := &config.SalmonCannonConfig{
		Bridges: []config.SalmonBridgeConfig{
			{Name: "bridge-one"},
			{Name: "bridge-two"},
		},
	}

	srv := NewServer(cfg, ":0")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/bridges", nil)
	w := httptest.NewRecorder()

	srv.handleBridges(w, req)

	res := w.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 got %d", res.StatusCode)
	}

	var list []bridgeDTO
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if got, want := len(list), 2; got != want {
		t.Fatalf("unexpected list length: got %d want %d", got, want)
	}

	if list[0].Name != "bridge-one" || list[0].ID != 0 {
		t.Fatalf("unexpected first element: %+v", list[0])
	}
	if list[1].Name != "bridge-two" || list[1].ID != 1 {
		t.Fatalf("unexpected second element: %+v", list[1])
	}
	if list[0].Circuit != "bridge-one" || list[0].ID != 0 {
		t.Fatalf("unexpected first element: %+v", list[0])
	}
	if list[1].Circuit != "bridge-two" || list[1].ID != 1 {
		t.Fatalf("unexpected second element: %+v", list[1])
	}
}
