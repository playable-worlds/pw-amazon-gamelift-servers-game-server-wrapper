// Package credserver provides a local HTTP credential server that the child process
// can query for refreshed AWS fleet-role credentials via AWS_CREDENTIAL_PROCESS.
package credserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// credentialProcessResponse is the JSON shape required by the AWS credential_process protocol.
type credentialProcessResponse struct {
	Version         int    `json:"Version"`
	AccessKeyId     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Expiration      string `json:"Expiration"`
}

// Server is a local credential server bound to 127.0.0.1 on a random port.
// Callers must present the bearer token in every request.
type Server struct {
	// Token is the bearer token the client must send in the Authorization header.
	Token string
	// Addr is the base URL of the server (e.g. "http://127.0.0.1:54321").
	Addr    string
	fetcher func(ctx context.Context) (accessKeyId, secretAccessKey, sessionToken string, expiration time.Time, err error)
	server  *http.Server
	logger  *slog.Logger
}

// New creates a Server with a cryptographically random bearer token.
// fetcher is called on every credential request to obtain fresh fleet-role credentials;
// pass nil on Anywhere fleets where GetFleetRoleCredentials is unavailable.
func New(logger *slog.Logger, fetcher func(ctx context.Context) (accessKeyId, secretAccessKey, sessionToken string, expiration time.Time, err error)) (*Server, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("failed to generate credential server token: %w", err)
	}
	return &Server{
		Token:   hex.EncodeToString(raw),
		fetcher: fetcher,
		logger:  logger,
	}, nil
}

// Start binds to a random loopback port and begins serving requests.
// It is non-blocking; the server runs in a background goroutine.
// Call Stop to shut it down.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("credential server listen failed: %w", err)
	}
	s.Addr = fmt.Sprintf("http://%s", ln.Addr().String())

	mux := http.NewServeMux()
	mux.HandleFunc("/credentials", s.handleCredentials)
	s.server = &http.Server{Handler: mux}

	go func() {
		if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logger.ErrorContext(ctx, "credential server stopped unexpectedly", "err", err)
		}
	}()

	s.logger.InfoContext(ctx, "credential server started", "addr", s.Addr)
	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop(ctx context.Context) {
	if s.server == nil {
		return
	}
	if err := s.server.Shutdown(ctx); err != nil {
		s.logger.ErrorContext(ctx, "credential server shutdown error", "err", err)
	}
}

func (s *Server) handleCredentials(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if s.fetcher == nil {
		http.Error(w, "credential fetcher not configured", http.StatusServiceUnavailable)
		return
	}

	accessKeyId, secretAccessKey, sessionToken, expiration, err := s.fetcher(r.Context())
	if err != nil {
		s.logger.ErrorContext(r.Context(), "GetFleetRoleCredentials failed", "err", err)
		http.Error(w, "failed to fetch credentials: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if expiration.IsZero() {
		// GameLift STS tokens last ~1 hour; use 55 min as a safe fallback.
		expiration = time.Now().Add(55 * time.Minute)
	}

	resp := credentialProcessResponse{
		Version:         1,
		AccessKeyId:     accessKeyId,
		SecretAccessKey: secretAccessKey,
		SessionToken:    sessionToken,
		Expiration:      expiration.UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.logger.ErrorContext(r.Context(), "failed to encode credential response", "err", err)
	}
}
