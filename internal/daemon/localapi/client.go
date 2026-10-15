package localapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hiberus-industria/forge/internal/auth"
)

// ErrDaemonDown means nothing is listening on the socket.
var ErrDaemonDown = errors.New("forge daemon is not running: start it with `forge daemon install` or `forge daemon run`")

// Client talks to the daemon over its unix socket.
type Client struct {
	hc *http.Client
	// stream has no timeout, for the SSE live feed.
	stream *http.Client
}

// NewClient returns a client for the daemon listening on socketPath.
func NewClient(socketPath string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{
		hc:     &http.Client{Transport: tr, Timeout: 10 * time.Second},
		stream: &http.Client{Transport: tr},
	}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://forge"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		var ne *net.OpError
		if errors.As(err, &ne) {
			return ErrDaemonDown
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e Error
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Message == "" {
			e.Message = resp.Status
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%w (%s)", auth.ErrNotLoggedIn, e.Message)
		}
		return errors.New(e.Message)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	return s, c.do(ctx, http.MethodGet, "/v1/status", nil, &s)
}

// Token implements auth.TokenSource by asking the daemon.
func (c *Client) Token(ctx context.Context) (auth.Token, error) {
	var t auth.Token
	return t, c.do(ctx, http.MethodGet, "/v1/token", nil, &t)
}

func (c *Client) ReloadCredentials(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/v1/credentials/reload", nil, nil)
}

func (c *Client) Recent(ctx context.Context, limit int) ([]LiveEvent, error) {
	var evs []LiveEvent
	return evs, c.do(ctx, http.MethodGet, fmt.Sprintf("/v1/live/recent?limit=%d", limit), nil, &evs)
}

// Override returns the current override, nil when none.
func (c *Client) Override(ctx context.Context) (*Override, error) {
	var o Override
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://forge/v1/override", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, ErrDaemonDown
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode >= 300 {
		return nil, errors.New(resp.Status)
	}
	return &o, json.NewDecoder(resp.Body).Decode(&o)
}

func (c *Client) SetOverride(ctx context.Context, r OverrideRequest) (Override, error) {
	var o Override
	return o, c.do(ctx, http.MethodPut, "/v1/override", r, &o)
}

func (c *Client) ClearOverride(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/v1/override", nil, nil)
}

func (c *Client) Sync(ctx context.Context) (SyncResult, error) {
	var r SyncResult
	return r, c.do(ctx, http.MethodPost, "/v1/sync", nil, &r)
}

// Live subscribes to the live feed. The channel is closed when ctx is done
// or the daemon goes away; callers typically resubscribe with backoff.
func (c *Client) Live(ctx context.Context) (<-chan LiveEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://forge/v1/live", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.stream.Do(req) //nolint:bodyclose // closed by the reader goroutine below
	if err != nil {
		return nil, ErrDaemonDown
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, errors.New(resp.Status)
	}
	ch := make(chan LiveEvent, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var ev LiveEvent
			if json.Unmarshal([]byte(data), &ev) != nil {
				continue
			}
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}
