package ui

import (
	"testing"

	"github.com/kbrdn1/LazyCurl/internal/api"
)

// TestLoadCollectionRequestSwitchesTabsForProtocol verifies loading a gRPC
// request swaps the tab set to Server/Metadata/Message/Scripts, loading an
// HTTP one keeps/restores Params/Authorization/Headers/Body/Scripts, and
// the method badge is locked to "INVOKE" for gRPC.
func TestLoadCollectionRequestSwitchesTabsForProtocol(t *testing.T) {
	rv := NewRequestView()

	grpcReq := &api.CollectionRequest{
		ID:       "req1",
		Name:     "Create Employee",
		Protocol: api.ProtocolGRPC,
		GRPC: &api.GRPCConfig{
			Server:  "localhost:29010",
			Service: "employee.v1.EmployeeService",
			Method:  "CreateEmployee",
		},
	}
	rv.LoadCollectionRequest(grpcReq)

	if got := rv.GetActiveTab(); got != "Server" {
		t.Errorf("GetActiveTab() = %q, want %q (first gRPC tab)", got, "Server")
	}
	if !rv.GetProtocol().IsGRPC() {
		t.Errorf("GetProtocol().IsGRPC() = false, want true")
	}
	if got := rv.GetMethod(); got != "INVOKE" {
		t.Errorf("GetMethod() = %q, want %q", got, "INVOKE")
	}

	httpReq := &api.CollectionRequest{
		ID:     "req2",
		Name:   "Get Users",
		Method: api.GET,
		URL:    "https://api.example.com/users",
	}
	rv.LoadCollectionRequest(httpReq)

	if got := rv.GetActiveTab(); got != "Params" {
		t.Errorf("GetActiveTab() = %q, want %q (first HTTP tab)", got, "Params")
	}
	if rv.GetProtocol().IsGRPC() {
		t.Errorf("GetProtocol().IsGRPC() = true, want false")
	}
	if got := rv.GetMethod(); got != "GET" {
		t.Errorf("GetMethod() = %q, want %q", got, "GET")
	}
}

// TestLoadCollectionRequestPopulatesServerRows verifies the fixed Server
// tab rows (Server/Service/Method/TLS) get populated from GRPCConfig, and
// that TLS's nested cert/key/CA/server-name/skip-verify rows only appear
// when TLS.Enabled is true.
func TestLoadCollectionRequestPopulatesServerRows(t *testing.T) {
	rv := NewRequestView()

	// No TLS - only the 4 base rows should exist
	rv.LoadCollectionRequest(&api.CollectionRequest{
		ID:       "req1",
		Protocol: api.ProtocolGRPC,
		GRPC: &api.GRPCConfig{
			Server:  "localhost:29010",
			Service: "employee.v1.EmployeeService",
			Method:  "CreateEmployee",
		},
	})
	cfg := rv.GetGRPCConfig()
	if cfg == nil {
		t.Fatalf("GetGRPCConfig() = nil")
	}
	if cfg.Server != "localhost:29010" || cfg.Service != "employee.v1.EmployeeService" || cfg.Method != "CreateEmployee" {
		t.Errorf("GetGRPCConfig() = %+v, want Server/Service/Method populated", cfg)
	}
	if cfg.TLS != nil {
		t.Errorf("GetGRPCConfig().TLS = %+v, want nil (TLS wasn't enabled)", cfg.TLS)
	}

	// TLS enabled with full mTLS config - sub-rows should round-trip
	rv.LoadCollectionRequest(&api.CollectionRequest{
		ID:       "req2",
		Protocol: api.ProtocolGRPC,
		GRPC: &api.GRPCConfig{
			Server: "localhost:29020",
			TLS: &api.GRPCTLSConfig{
				Enabled:            true,
				CertFile:           "/certs/client.crt",
				KeyFile:            "/certs/client.key",
				CAFile:             "/certs/ca.crt",
				ServerName:         "localhost",
				InsecureSkipVerify: true,
			},
		},
	})
	cfg = rv.GetGRPCConfig()
	if cfg == nil || cfg.TLS == nil {
		t.Fatalf("GetGRPCConfig() = %+v, want populated TLS", cfg)
	}
	if !cfg.TLS.Enabled {
		t.Errorf("TLS.Enabled = false, want true")
	}
	if cfg.TLS.CertFile != "/certs/client.crt" {
		t.Errorf("TLS.CertFile = %q, want %q", cfg.TLS.CertFile, "/certs/client.crt")
	}
	if cfg.TLS.KeyFile != "/certs/client.key" {
		t.Errorf("TLS.KeyFile = %q, want %q", cfg.TLS.KeyFile, "/certs/client.key")
	}
	if cfg.TLS.CAFile != "/certs/ca.crt" {
		t.Errorf("TLS.CAFile = %q, want %q", cfg.TLS.CAFile, "/certs/ca.crt")
	}
	if cfg.TLS.ServerName != "localhost" {
		t.Errorf("TLS.ServerName = %q, want %q", cfg.TLS.ServerName, "localhost")
	}
	if !cfg.TLS.InsecureSkipVerify {
		t.Errorf("TLS.InsecureSkipVerify = false, want true")
	}
}

// TestSyncGRPCTLSRowsAddsAndRemovesSubRows verifies flipping the TLS row's
// value adds the 5 nested sub-rows when enabled and strips them back off
// when disabled again, without disturbing the 4 base rows.
func TestSyncGRPCTLSRowsAddsAndRemovesSubRows(t *testing.T) {
	rv := NewRequestView()
	rv.LoadCollectionRequest(&api.CollectionRequest{
		ID:       "req1",
		Protocol: api.ProtocolGRPC,
		GRPC:     &api.GRPCConfig{Server: "localhost:29010"},
	})

	if cfg := rv.GetGRPCConfig(); cfg.TLS != nil {
		t.Fatalf("starting state has TLS = %+v, want nil", cfg.TLS)
	}

	// Simulate the "TLS" row's value being edited to "true"
	rv.UpdateRow(3, "TLS", "true")
	rv.SyncGRPCTLSRows()

	cfg := rv.GetGRPCConfig()
	if cfg.TLS == nil || !cfg.TLS.Enabled {
		t.Fatalf("after enabling TLS, GetGRPCConfig().TLS = %+v, want Enabled", cfg.TLS)
	}

	// Flip it back off
	rv.UpdateRow(3, "TLS", "false")
	rv.SyncGRPCTLSRows()

	cfg = rv.GetGRPCConfig()
	if cfg.TLS != nil {
		t.Errorf("after disabling TLS, GetGRPCConfig().TLS = %+v, want nil", cfg.TLS)
	}
	// Server field (base row 0) must survive the round trip untouched
	if cfg.Server != "localhost:29010" {
		t.Errorf("Server = %q, want %q (base rows shouldn't be disturbed)", cfg.Server, "localhost:29010")
	}
}
