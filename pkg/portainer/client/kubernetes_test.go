package client

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/portainer/portainer-mcp/pkg/portainer/models"
	"github.com/stretchr/testify/assert"
)

// TestProxyKubernetesRequest mirrors TestProxyDockerRequest. Both tools
// share the same rawHTTPClient proxy path, the only difference being
// the URL template (.../docker… vs .../kubernetes…).
func TestProxyKubernetesRequest(t *testing.T) {
	tests := []struct {
		name             string
		opts             models.KubernetesProxyRequestOptions
		expectedPath     string
		expectedQuery    string
		responseStatus   int
		responseBody     string
		expectedRespBody string
	}{
		{
			name: "GET request with query parameters",
			opts: models.KubernetesProxyRequestOptions{
				EnvironmentID: 1,
				Method:        "GET",
				Path:          "/api/v1/namespaces",
				QueryParams:   map[string]string{"limit": "100"},
			},
			expectedPath:     "/api/endpoints/1/kubernetes/api/v1/namespaces",
			expectedQuery:    "limit=100",
			responseStatus:   http.StatusOK,
			responseBody:     `{"items":[]}`,
			expectedRespBody: `{"items":[]}`,
		},
		{
			name: "POST request with custom headers and body",
			opts: models.KubernetesProxyRequestOptions{
				EnvironmentID: 2,
				Method:        "POST",
				Path:          "/api/v1/namespaces/default/pods",
				Headers:       map[string]string{"X-Test": "yes"},
				Body:          bytes.NewBufferString(`{"kind":"Pod"}`),
			},
			expectedPath:     "/api/endpoints/2/kubernetes/api/v1/namespaces/default/pods",
			expectedQuery:    "",
			responseStatus:   http.StatusCreated,
			responseBody:     `{"metadata":{"name":"test"}}`,
			expectedRespBody: `{"metadata":{"name":"test"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, tt.expectedPath, r.URL.Path)
				assert.Equal(t, tt.opts.Method, r.Method)
				assert.Equal(t, "test-token", r.Header.Get("x-api-key"))
				if tt.expectedQuery != "" {
					assert.Equal(t, tt.expectedQuery, r.URL.RawQuery)
				}
				for k, v := range tt.opts.Headers {
					assert.Equal(t, v, r.Header.Get(k))
				}
				w.WriteHeader(tt.responseStatus)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			client := newTestClient(t, server.URL)
			resp, err := client.ProxyKubernetesRequest(tt.opts)
			assert.NoError(t, err)
			if assert.NotNil(t, resp) {
				defer func() { _ = resp.Body.Close() }()
				assert.Equal(t, tt.responseStatus, resp.StatusCode)
				body, _ := io.ReadAll(resp.Body)
				assert.Equal(t, tt.expectedRespBody, string(body))
			}
		})
	}
}
