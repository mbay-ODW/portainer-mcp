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
// Authentication (configured via env vars):
//   - MCP_API_KEY              : static Bearer token (optional fallback)
//   - OIDC_INTROSPECTION_URL   : Authelia introspection endpoint (RFC 7662)
//   - OIDC_CLIENT_ID           : client id used for introspection auth
//   - OIDC_CLIENT_SECRET       : client secret used for introspection auth
//
// Discovery (RFC 9728 + RFC 8414, both unauthenticated):
//   - OAUTH_ISSUER             : public Authelia base URL
//   - MCP_SERVER_URL           : public URL of this MCP server
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
	oauthIssuer := os.Getenv("OAUTH_ISSUER")
	mcpServerURL := os.Getenv("MCP_SERVER_URL")

	authConfigured := mcpAPIKey != "" || (oidcIntrospectionURL != "" && oidcClientID != "" && oidcClientSecret != "")

	log.Info().
		Bool("static_token", mcpAPIKey != "").
		Bool("oidc_introspection", oidcIntrospectionURL != "" && oidcClientID != "" && oidcClientSecret != "").
		Bool("oauth_discovery", oauthIssuer != "" && mcpServerURL != "").
		Msg("SSE auth config")

	sseServer := server.NewSSEServer(s.srv)

	// Auth middleware – wraps SSE + message handlers.
	authMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !authConfigured {
				next.ServeHTTP(w, r)
				return
			}
			if isAuthorized(r, mcpAPIKey, oidcIntrospectionURL, oidcClientID, oidcClientSecret) {
				next.ServeHTTP(w, r)
				return
			}
			log.Warn().Str("path", r.URL.Path).Msg("auth denied")
			w.Header().Set("WWW-Authenticate", `Bearer realm="portainer-mcp"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
		})
	}

	mux := http.NewServeMux()

	// RFC 9728 – OAuth 2.0 Protected Resource Metadata.
	// Claude.ai fetches this FIRST to discover which authorization server
	// protects this resource. Must be public (no auth).
	if oauthIssuer != "" && mcpServerURL != "" {
		mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource":                 mcpServerURL,
				"authorization_servers":    []string{oauthIssuer},
				"bearer_methods_supported": []string{"header"},
				"scopes_supported":         []string{"openid", "profile", "email"},
			})
		})
	}

	// RFC 8414 – OAuth 2.0 Authorization Server Metadata.
	// Describes all Authelia endpoints Claude.ai needs for the OAuth PKCE flow.
	if oauthIssuer != "" {
		mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                           oauthIssuer,
				"authorization_endpoint":           oauthIssuer + "/api/oidc/authorization",
				"token_endpoint":                   oauthIssuer + "/api/oidc/token",
				"jwks_uri":                         oauthIssuer + "/jwks.json",
				"introspection_endpoint":           oauthIssuer + "/api/oidc/introspection",
				"response_types_supported":         []string{"code"},
				"grant_types_supported":            []string{"authorization_code", "refresh_token"},
				"code_challenge_methods_supported": []string{"S256"},
				"scopes_supported":                 []string{"openid", "profile", "email"},
			})
		})
	}

	// Authenticated MCP endpoints
	mux.Handle("/sse", authMiddleware(sseServer.SSEHandler()))
	mux.Handle("/message", authMiddleware(sseServer.MessageHandler()))

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
		log.Debug().Msg("static Bearer token matched")
		return true
	}

	if !strings.HasPrefix(auth, "Bearer ") {
		log.Debug().Msg("Authorization header is not a Bearer token")
		return false
	}

	if introspectURL == "" || clientID == "" || clientSecret == "" {
		log.Debug().Msg("OIDC introspection not fully configured")
		return false
	}

	token := strings.TrimPrefix(auth, "Bearer ")
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

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Error().Err(err).Msg("introspection request failed")
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Warn().Int("status", resp.StatusCode).Msg("introspection returned non-200")
		return false
	}

	var data struct {
		Active bool `json:"active"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		log.Error().Err(err).Msg("failed to decode introspection response")
		return false
	}
	if !data.Active {
		log.Debug().Msg("OIDC token not active")
		return false
	}
	return true
}
