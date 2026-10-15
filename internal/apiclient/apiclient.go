// Package apiclient builds an authenticated client for the forge API.
package apiclient

import (
	"context"
	"net/http"
	"time"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/auth"
)

// New returns a typed forge API client that adds a bearer token from ts to
// every request.
func New(baseURL string, ts auth.TokenSource, userAgent string) (*api.ClientWithResponses, error) {
	return api.NewClientWithResponses(baseURL,
		api.WithHTTPClient(&http.Client{Timeout: 30 * time.Second}),
		api.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
			t, err := ts.Token(ctx)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+t.AccessToken)
			req.Header.Set("User-Agent", userAgent)
			return nil
		}),
	)
}
