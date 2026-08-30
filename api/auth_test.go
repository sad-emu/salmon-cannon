package api

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"salmoncannon/config"
)

func TestLoadBasicAuthenticatorHashesPlaintextAndAuthenticates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	contents := `{
  "users": [
    {"username": "admin", "password": "correct horse battery staple"}
  ]
}`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	auth, err := loadBasicAuthenticator(path)
	if err != nil {
		t.Fatalf("loadBasicAuthenticator failed: %v", err)
	}

	var rewritten passwordFile
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "correct horse battery staple") {
		t.Fatal("rewritten auth file still contains the plaintext password")
	}
	if err := json.Unmarshal(data, &rewritten); err != nil {
		t.Fatalf("rewritten auth file is invalid JSON: %v", err)
	}
	if len(rewritten.Users) != 1 || rewritten.Users[0].PasswordSHA512 == "" || rewritten.Users[0].Salt == "" {
		t.Fatalf("rewritten credentials are incomplete: %+v", rewritten.Users)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0600 {
		t.Fatalf("rewritten auth file mode = %o, want 600", info.Mode().Perm())
	}

	valid := httptest.NewRequest(http.MethodGet, "/", nil)
	valid.SetBasicAuth("admin", "correct horse battery staple")
	if got := auth.authenticate(valid); !got.ok || got.username != "admin" {
		t.Fatalf("valid credentials were rejected: %+v", got)
	}

	invalid := httptest.NewRequest(http.MethodGet, "/", nil)
	invalid.SetBasicAuth("admin", "wrong")
	if got := auth.authenticate(invalid); got.ok {
		t.Fatal("invalid password was accepted")
	}

	// Loading a second time must accept the persisted hash without rewriting it.
	if _, err := loadBasicAuthenticator(path); err != nil {
		t.Fatalf("could not reload hashed credentials: %v", err)
	}
}

func TestBasicAuthenticationMiddleware(t *testing.T) {
	salt := []byte("test salt")
	hash := saltedPasswordHash(salt, "secret")
	srv := &Server{auth: &basicAuthenticator{users: map[string]passwordCredential{
		"alice": {salt: salt, hash: hash},
	}}}
	handler := srv.requireAuthentication(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("missing credentials status = %d, want 401", unauthorized.Code)
	}
	if !strings.HasPrefix(unauthorized.Header().Get("WWW-Authenticate"), "Basic ") {
		t.Fatal("missing Basic authentication challenge")
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	authorizedRequest.SetBasicAuth("alice", "secret")
	authorized := httptest.NewRecorder()
	handler.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusNoContent {
		t.Fatalf("valid credentials status = %d, want 204", authorized.Code)
	}
}

func TestMTLSAuthenticatorMatchesPartialVerifiedSubjectDN(t *testing.T) {
	auth := &mtlsAuthenticator{users: []mtlsFileUser{
		{DN: "OU=Operations,O=Example Corp", Username: "deploy-bot"},
	}}
	cert := &x509.Certificate{Subject: pkix.Name{
		CommonName:         "client-17",
		OrganizationalUnit: []string{"Operations"},
		Organization:       []string{"Example Corp"},
	}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	}
	if got := auth.authenticate(req); !got.ok || got.username != "deploy-bot" {
		t.Fatalf("verified matching certificate was rejected: subject=%q result=%+v", cert.Subject.String(), got)
	}

	req.TLS.VerifiedChains = nil
	if got := auth.authenticate(req); got.ok {
		t.Fatal("unverified certificate was accepted")
	}
}

func TestMTLSServerTLSConfigRequiresAndVerifiesClientCertificates(t *testing.T) {
	certFile, keyFile := generateTestCert(t)
	srv := NewServer(testConfigWithAPI(), "127.0.0.1:0")
	srv.cfg.ApiConfig.TLSCert = certFile
	srv.cfg.ApiConfig.TLSKey = keyFile
	srv.cfg.ApiConfig.TLSClientCA = certFile
	srv.cfg.ApiConfig.MTLSAuthFile = "mtls-users.json"

	tlsConfig, err := srv.serverTLSConfig()
	if err != nil {
		t.Fatalf("serverTLSConfig failed: %v", err)
	}
	if tlsConfig.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("ClientAuth = %v, want RequireAndVerifyClientCert", tlsConfig.ClientAuth)
	}
	if tlsConfig.ClientCAs == nil || len(tlsConfig.ClientCAs.Subjects()) != 1 {
		t.Fatal("client CA was not loaded")
	}
}

func TestServerRejectsIncompleteTLSAndConflictingAuth(t *testing.T) {
	srv := NewServer(testConfigWithAPI(), "127.0.0.1:0")
	srv.cfg.ApiConfig.TLSCert = "server.pem"
	if err := srv.Start(); err == nil || !strings.Contains(err.Error(), "configured together") {
		t.Fatalf("incomplete TLS configuration error = %v", err)
	}

	srv = NewServer(testConfigWithAPI(), "127.0.0.1:0")
	srv.cfg.ApiConfig.BasicAuthFile = "basic.json"
	srv.cfg.ApiConfig.MTLSAuthFile = "mtls.json"
	if err := srv.loadAuthenticator(); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("conflicting auth configuration error = %v", err)
	}
}

func testConfigWithAPI() *config.SalmonCannonConfig {
	return &config.SalmonCannonConfig{ApiConfig: &config.ApiConfig{}}
}
