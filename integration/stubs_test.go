//go:build unix

package integration_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"google.golang.org/grpc"

	"github.com/mediusfy/modulex/examples/deployment/notification/notificationpb"
)

// messageLog is a goroutine-safe record of notification messages a stub
// received.
type messageLog struct {
	mu   sync.Mutex
	msgs []string
}

func (l *messageLog) add(m string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, m)
}

func (l *messageLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.msgs...)
}

// startHTTPNotificationStub serves the notification service's POST /notify
// contract (202 on success) and records every message.
func startHTTPNotificationStub(t *testing.T) (*httptest.Server, *messageLog) {
	t.Helper()
	log := &messageLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message string `json:"message"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/notify" || json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		log.add(body.Message)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

type recordingNotificationServer struct {
	notificationpb.UnimplementedNotificationServer
	log *messageLog
}

func (s recordingNotificationServer) Send(_ context.Context, req *notificationpb.SendRequest) (*notificationpb.SendResponse, error) {
	s.log.add(req.GetMessage())
	return &notificationpb.SendResponse{}, nil
}

// startGRPCNotificationStub serves the Notification gRPC service on a
// loopback port and records every message. It returns host:port.
func startGRPCNotificationStub(t *testing.T) (string, *messageLog) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	log := &messageLog{}
	srv := grpc.NewServer()
	notificationpb.RegisterNotificationServer(srv, recordingNotificationServer{log: log})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String(), log
}

// startNATS runs an in-process JetStream-enabled NATS server on a random
// loopback port and returns its client URL.
func startNATS(t *testing.T) string {
	t.Helper()
	storeDir := t.TempDir() // registered before Shutdown so it is removed after it
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  storeDir,
		NoLog:     true,
		NoSigs:    true,
	})
	if err != nil {
		t.Fatalf("new nats server: %v", err)
	}
	go srv.Start()
	t.Cleanup(func() {
		srv.Shutdown()
		srv.WaitForShutdown()
	})
	if !srv.ReadyForConnections(15 * time.Second) {
		t.Fatal("in-process nats server not ready within 15s")
	}
	return srv.ClientURL()
}

var (
	appKeyOnce sync.Once
	appKeyPEM  string
	appKeyErr  error
)

// githubAppKeyPEM returns a freshly generated (throwaway) RSA private key in
// PKCS#1 PEM form, shaped like a GitHub App private key.
func githubAppKeyPEM(t *testing.T) string {
	t.Helper()
	appKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			appKeyErr = err
			return
		}
		appKeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	})
	if appKeyErr != nil {
		t.Fatalf("generate app key: %v", appKeyErr)
	}
	return appKeyPEM
}

// fakeADCFile writes an "authorized_user" Application Default Credentials
// file with made-up values and returns its path. Google client constructors
// accept it without any network access; a token is only fetched on the first
// RPC, which the hermetic rows never make.
func fakeADCFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adc.json")
	const creds = `{"type":"authorized_user","client_id":"smoke","client_secret":"smoke","refresh_token":"smoke"}`
	if err := os.WriteFile(path, []byte(creds), 0o600); err != nil {
		t.Fatalf("write fake ADC: %v", err)
	}
	return path
}
