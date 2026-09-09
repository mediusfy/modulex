package githubauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestParsePrivateKey(t *testing.T) {
	key := testKey(t)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes})

	tests := []struct {
		name    string
		pem     []byte
		wantErr bool
	}{
		{name: "PKCS#1 (GitHub App default)", pem: pkcs1},
		{name: "PKCS#8", pem: pkcs8},
		{name: "not PEM", pem: []byte("garbage"), wantErr: true},
		{name: "empty", pem: nil, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePrivateKey(tt.pem)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestInstallationTokenScopedToInstallation(t *testing.T) {
	tests := []struct {
		name           string
		installationID int64
		respStatus     int
		respBody       string
		want           string
		wantErr        bool
	}{
		{
			name:           "token minted for exactly the requested installation",
			installationID: 42,
			respStatus:     http.StatusCreated,
			respBody:       `{"token": "ghs_scoped"}`,
			want:           "ghs_scoped",
		},
		{
			name:           "GitHub error surfaces",
			installationID: 42,
			respStatus:     http.StatusUnauthorized,
			respBody:       `{"message": "bad jwt"}`,
			wantErr:        true,
		},
		{
			name:           "missing token in response is an error",
			installationID: 42,
			respStatus:     http.StatusCreated,
			respBody:       `{}`,
			wantErr:        true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				w.WriteHeader(tt.respStatus)
				_, _ = w.Write([]byte(tt.respBody))
			}))
			defer srv.Close()

			a := &AppAuth{AppID: "1234", PrivateKey: testKey(t), BaseURL: srv.URL, HTTP: srv.Client()}
			token, err := a.InstallationToken(context.Background(), tt.installationID)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if token != tt.want {
				t.Fatalf("token %q, want %q", token, tt.want)
			}
			if gotPath != "/app/installations/42/access_tokens" {
				t.Fatalf("path %q not scoped to installation 42", gotPath)
			}
			// The bearer must be a well-formed RS256 App JWT with our issuer.
			jwt := strings.TrimPrefix(gotAuth, "Bearer ")
			parts := strings.Split(jwt, ".")
			if len(parts) != 3 {
				t.Fatalf("JWT has %d parts", len(parts))
			}
			claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err != nil {
				t.Fatal(err)
			}
			var claims struct {
				Iss string `json:"iss"`
				Exp int64  `json:"exp"`
				Iat int64  `json:"iat"`
			}
			if err := json.Unmarshal(claimsJSON, &claims); err != nil {
				t.Fatal(err)
			}
			if claims.Iss != "1234" {
				t.Fatalf("iss %q, want 1234", claims.Iss)
			}
			if lifetime := time.Duration(claims.Exp-claims.Iat) * time.Second; lifetime > 10*time.Minute {
				t.Fatalf("JWT lifetime %s exceeds GitHub's 10m cap", lifetime)
			}
		})
	}
}

func TestRESTCommenter(t *testing.T) {
	tests := []struct {
		name       string
		call       func(c *RESTCommenter, base string) error
		wantMethod string
		wantPath   string
		respStatus int
		respBody   string
		wantErr    bool
	}{
		{
			name: "create posts to the PR's comments",
			call: func(c *RESTCommenter, _ string) error {
				id, err := c.CreateComment(context.Background(), "tok", "o", "r", 7, "hello")
				if err == nil && id != 99 {
					t.Fatalf("id %d, want 99", id)
				}
				return err
			},
			wantMethod: http.MethodPost,
			wantPath:   "/repos/o/r/issues/7/comments",
			respStatus: http.StatusCreated,
			respBody:   `{"id": 99}`,
		},
		{
			name: "update patches the existing comment",
			call: func(c *RESTCommenter, _ string) error {
				return c.UpdateComment(context.Background(), "tok", "o", "r", 99, "hello again")
			},
			wantMethod: http.MethodPatch,
			wantPath:   "/repos/o/r/issues/comments/99",
			respStatus: http.StatusOK,
			respBody:   `{"id": 99}`,
		},
		{
			name: "GitHub failure surfaces",
			call: func(c *RESTCommenter, _ string) error {
				_, err := c.CreateComment(context.Background(), "tok", "o", "r", 7, "hello")
				return err
			},
			wantMethod: http.MethodPost,
			wantPath:   "/repos/o/r/issues/7/comments",
			respStatus: http.StatusForbidden,
			respBody:   `{"message": "nope"}`,
			wantErr:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMethod, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				w.WriteHeader(tt.respStatus)
				_, _ = w.Write([]byte(tt.respBody))
			}))
			defer srv.Close()

			c := &RESTCommenter{BaseURL: srv.URL, HTTP: srv.Client()}
			err := tt.call(c, srv.URL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if gotMethod != tt.wantMethod || gotPath != tt.wantPath {
				t.Fatalf("%s %s, want %s %s", gotMethod, gotPath, tt.wantMethod, tt.wantPath)
			}
		})
	}
}
