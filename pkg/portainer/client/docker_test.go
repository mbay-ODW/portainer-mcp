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

// TestProxyDockerRequest exercises the rawHTTPClient proxy path. We can't
// use the upstream client-api-go ProxyClient because it hardcodes
// "https://" and ignores client.WithScheme(); see docker.go for the full
// reasoning. The tests stand up an httptest server and assert that the
// request URL, method, headers and query parameters are forwarded
// unchanged through the Portainer proxy URL template
// /api/endpoints/{id}/docker{path}.
func TestProxyDockerRequest(t *testing.T) {
	tests := []struct {
		name             string
		opts             models.DockerProxyRequestOptions
		expectedPath     string
		expectedQuery    string
		responseStatus   int
		responseBody     string
		expectedRespBody string
	}{
		{
			name: "GET request with query parameters",
			opts: models.DockerProxyRequestOptions{
				EnvironmentID: 1,
				Method:        "GET",
				Path:          "/images/json",
				QueryParams:   map[string]string{"all": "true"},
			},
			expectedPath:     "/api/endpoints/1/docker/images/json",
			expectedQuery:    "all=true",
			responseStatus:   http.StatusOK,
			responseBody:     `[{"Id":"img1"}]`,
			expectedRespBody: `[{"Id":"img1"}]`,
		},
		{
			name: "POST request with custom headers and body",
			opts: models.DockerProxyRequestOptions{
				EnvironmentID: 2,
				Method:        "POST",
				Path:          "/networks/create",
				Headers:       map[string]string{"X-Custom-Header": "value1"},
				Body:          bytes.NewBufferString(`{"Name": "my-network"}`),
			},
			expectedPath:     "/api/endpoints/2/docker/networks/create",
			expectedQuery:    "",
			responseStatus:   http.StatusCreated,
			responseBody:     `{"Id": "net1"}`,
			expectedRespBody: `{"Id": "net1"}`,
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
			resp, err := client.ProxyDockerRequest(tt.opts)
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
