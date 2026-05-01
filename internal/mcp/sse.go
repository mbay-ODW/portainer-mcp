package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog/log"
)

// StartSSE begins listening for MCP protocol messages over Server-Sent Events.
// This is a blocking call that runs until the HTTP server is closed.
//
// Implementation deliberately mirrors the hero-mcp Python server: simple
// 401 with no body and no WWW-Authenticate header, no OAuth discovery
// endpoints. Auth happens entirely via OIDC token introspection against
// Authelia (or a static fallback Bearer token for Claude Desktop).
//
// Authentication (configured via env vars):
//   - MCP_API_KEY              : static Bearer token (optional fallback)
//   - OIDC_INTROSPECTION_URL   : Authelia introspection endpoint (RFC 7662)
//   - OIDC_CLIENT_ID           : client id used for introspection auth
//   - OIDC_CLIENT_SECRET       : client secret used for introspection auth
//
// Auth order on incoming requests:
//  1. If MCP_API_KEY is set and Authorization == "Bearer <MCP_API_KEY>" → allow
//  2. If a Bearer token is presented and OIDC vars are set → introspect; allow if active
//  3. Otherwise → 401
//
// If MCP_API_KEY is empty and OIDC is not configured, all requests are allowed
// (useful for local development).
func (s *PortainerMCPServer) StartSSE(addr string) error {
	mcpAPIKey := os.Getenv("MCP_API_KEY")
	oidcIntrospectionURL := os.Getenv("OIDC_INTROSPECTION_URL")
	oidcClientID := os.Getenv("OIDC_CLIENT_ID")
	oidcClientSecret := os.Getenv("OIDC_CLIENT_SECRET")

	authConfigured := mcpAPIKey != "" || (oidcIntrospectionURL != "" && oidcClientID != "" && oidcClientSecret != "")

	log.Info().
		Bool("static_token", mcpAPIKey != "").
		Bool("oidc_introspection", oidcIntrospectionURL != "" && oidcClientID != "" && oidcClientSecret != "").
		Msg("SSE auth config")

	// "/sse" + "/messages/" – same convention as the Python/TS MCP SDKs and
	// the other MCP servers in this stack (used by Claude Desktop and older
	// Claude.ai clients).
	sseServer := server.NewSSEServer(s.srv,
		server.WithMessageEndpoint("/messages/"),
		server.WithSSEEndpoint("/sse"),
	)

	// Streamable HTTP transport (current MCP spec). Claude.ai probes
	// /mcp with POST/GET first; if we don't speak it the client falls into
	// a reconnect loop on /sse.
	streamableServer := server.NewStreamableHTTPServer(s.srv,
		server.WithEndpointPath("/mcp"),
	)

	authMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			authHeader := r.Header.Get("Authorization")
			authPreview := "(none)"
			if authHeader != "" {
				if len(authHeader) > 20 {
					authPreview = authHeader[:20] + "…"
				} else {
					authPreview = authHeader
				}
			}
			log.Debug().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Str("remote", r.RemoteAddr).
				Str("authorization", authPreview).
				Msg("incoming request")

			if !authConfigured {
				log.Debug().Msg("auth not configured – passing through")
				next.ServeHTTP(w, r)
				return
			}
			if isAuthorized(r, mcpAPIKey, oidcIntrospectionURL, oidcClientID, oidcClientSecret) {
				log.Debug().Dur("auth_elapsed", time.Since(start)).Msg("auth ok")
				next.ServeHTTP(w, r)
				return
			}
			log.Warn().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Str("authorization", authPreview).
				Dur("auth_elapsed", time.Since(start)).
				Msg("auth denied – returning 401")
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
		})
	}

	mux := http.NewServeMux()
	// Streamable HTTP (preferred by current Claude.ai clients).
	mux.Handle("/mcp", authMiddleware(streamableServer))
	// /sse: dispatch by method so a single connector URL covers both
	// transports.
	//   POST /sse  → Streamable HTTP (current spec, used by claude.ai)
	//   GET  /sse  → classic SSE handshake (Claude Desktop / older clients)
	// This matches the behaviour of the Python MCP servers in this stack
	// (hero-mcp, mail-mcp, whatsapp-mcp, paperless-mcp).
	mux.Handle("/sse", authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			streamableServer.ServeHTTP(w, r)
			return
		}
		sseServer.SSEHandler().ServeHTTP(w, r)
	})))
	mux.Handle("/messages/", authMiddleware(sseServer.MessageHandler()))

	log.Info().Str("addr", addr).Msg("portainer-mcp SSE server listening")
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return httpServer.ListenAndServe()
}

// isAuthorized returns true if the incoming request carries a valid Bearer token.
// Either matches the static MCP_API_KEY or is an active OIDC token per introspection.
func isAuthorized(r *http.Request, staticKey, introspectURL, clientID, clientSecret string) bool {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		log.Debug().Msg("no Authorization header")
		return false
	}

	if staticKey != "" && auth == "Bearer "+staticKey {
		log.Debug().Msg("static MCP_API_KEY Bearer token matched")
		return true
	}

	if !strings.HasPrefix(auth, "Bearer ") {
		log.Debug().Str("scheme", strings.SplitN(auth, " ", 2)[0]).Msg("Authorization header is not a Bearer token")
		return false
	}

	if introspectURL == "" || clientID == "" || clientSecret == "" {
		log.Warn().
			Bool("introspect_url_set", introspectURL != "").
			Bool("client_id_set", clientID != "").
			Bool("client_secret_set", clientSecret != "").
			Msg("Bearer token presented but OIDC introspection not fully configured")
		return false
	}

	token := strings.TrimPrefix(auth, "Bearer ")
	log.Debug().Int("token_len", len(token)).Str("introspect_url", introspectURL).Msg("calling OIDC introspection")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	form := url.Values{}
	form.Set("token", token)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, introspectURL, strings.NewReader(form.Encode()))
	if err != nil {
		log.Error().Err(err).Msg("failed to build introspection request")
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(clientID, clientSecret)

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Error().Err(err).Dur("elapsed", time.Since(start)).Msg("introspection request failed")
		return false
	}
	defer resp.Body.Close()
	log.Debug().Int("status", resp.StatusCode).Dur("elapsed", time.Since(start)).Msg("introspection HTTP response")

	if resp.StatusCode != http.StatusOK {
		log.Warn().Int("status", resp.StatusCode).Msg("introspection returned non-200 – denying")
		return false
	}

	var data struct {
		Active bool   `json:"active"`
		Sub    string `json:"sub,omitempty"`
		Scope  string `json:"scope,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Error().Err(err).Msg("failed to decode introspection response")
		return false
	}
	if !data.Active {
		log.Warn().Msg("OIDC token not active – denying")
		return false
	}
	log.Debug().Str("sub", data.Sub).Str("scope", data.Scope).Msg("OIDC token active – allowing")
	return true
}
