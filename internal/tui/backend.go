package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/auth"
	"github.com/hiberus-industria/forge/internal/daemon/localapi"
	"github.com/hiberus-industria/forge/internal/devflow"
	"github.com/hiberus-industria/forge/internal/domain"
)

// Backend is everything the TUI reads and does. The live implementation
// wraps the daemon client, the forge API client and devflow; the demo
// implementation (FORGE_TUI_DEMO=1) fakes it all.
type Backend interface {
	Status(ctx context.Context) (localapi.Status, error)
	Recent(ctx context.Context, limit int) ([]localapi.LiveEvent, error)
	Live(ctx context.Context) (<-chan localapi.LiveEvent, error)
	SetOverride(ctx context.Context, r localapi.OverrideRequest) (localapi.Override, error)
	ClearOverride(ctx context.Context) error

	Tasks(ctx context.Context, p api.ListTasksParams) ([]api.Task, error)
	Task(ctx context.Context, ref domain.IssueRef) (api.TaskDetail, error)
	Inbox(ctx context.Context, p api.ListInboxParams) ([]api.InboxGroup, error)
	Reassign(ctx context.Context, r api.ReassignRequest) (api.ReassignResult, error)
	// UnassignTask sends the caller's events of an issue back to the inbox.
	UnassignTask(ctx context.Context, ref domain.IssueRef) (api.ReassignResult, error)
	Report(ctx context.Context, p api.GetCostReportParams) (api.CostReport, error)

	Start(ctx context.Context, o devflow.StartOptions) (devflow.StartResult, error)
	OpenPR(ctx context.Context, o devflow.PROptions) (devflow.PRResult, error)
}

type liveBackend struct {
	d    *localapi.Client
	c    *api.ClientWithResponses
	flow devflow.Service
}

func (b liveBackend) Status(ctx context.Context) (localapi.Status, error) { return b.d.Status(ctx) }
func (b liveBackend) Recent(ctx context.Context, n int) ([]localapi.LiveEvent, error) {
	return b.d.Recent(ctx, n)
}

func (b liveBackend) Live(ctx context.Context) (<-chan localapi.LiveEvent, error) {
	return b.d.Live(ctx)
}

func (b liveBackend) SetOverride(ctx context.Context, r localapi.OverrideRequest) (localapi.Override, error) {
	return b.d.SetOverride(ctx, r)
}
func (b liveBackend) ClearOverride(ctx context.Context) error { return b.d.ClearOverride(ctx) }

func (b liveBackend) Tasks(ctx context.Context, p api.ListTasksParams) ([]api.Task, error) {
	r, err := b.c.ListTasksWithResponse(ctx, &p)
	if err := apiErr(r, err, r != nil && r.JSON200 != nil, func() *api.Error { return r.JSONDefault }); err != nil {
		return nil, err
	}
	return *r.JSON200, nil
}

func (b liveBackend) Task(ctx context.Context, ref domain.IssueRef) (api.TaskDetail, error) {
	r, err := b.c.GetTaskWithResponse(ctx, ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err := apiErr(r, err, r != nil && r.JSON200 != nil, func() *api.Error { return r.JSONDefault }); err != nil {
		return api.TaskDetail{}, err
	}
	return *r.JSON200, nil
}

func (b liveBackend) Inbox(ctx context.Context, p api.ListInboxParams) ([]api.InboxGroup, error) {
	r, err := b.c.ListInboxWithResponse(ctx, &p)
	if err := apiErr(r, err, r != nil && r.JSON200 != nil, func() *api.Error { return r.JSONDefault }); err != nil {
		return nil, err
	}
	return *r.JSON200, nil
}

func (b liveBackend) Reassign(ctx context.Context, req api.ReassignRequest) (api.ReassignResult, error) {
	r, err := b.c.ReassignUsageEventsWithResponse(ctx, req)
	if err := apiErr(r, err, r != nil && r.JSON200 != nil, func() *api.Error { return r.JSONDefault }); err != nil {
		return api.ReassignResult{}, err
	}
	return *r.JSON200, nil
}

func (b liveBackend) UnassignTask(ctx context.Context, ref domain.IssueRef) (api.ReassignResult, error) {
	me, repo, limit := "me", ref.Repo.String(), 500
	issue := ref.Number
	p := api.ListUsageEventsParams{User: &me, Repo: &repo, Issue: &issue, Limit: &limit}
	var ids []api.UsageEvent
	for {
		r, err := b.c.ListUsageEventsWithResponse(ctx, &p)
		if err := apiErr(r, err, r != nil && r.JSON200 != nil, func() *api.Error { return r.JSONDefault }); err != nil {
			return api.ReassignResult{}, err
		}
		ids = append(ids, r.JSON200.Items...)
		if r.JSON200.NextCursor == nil || *r.JSON200.NextCursor == "" {
			break
		}
		p.Cursor = r.JSON200.NextCursor
	}
	if len(ids) == 0 {
		return api.ReassignResult{}, nil
	}
	req := api.ReassignRequest{}
	for _, e := range ids {
		req.EventIds = append(req.EventIds, e.Id)
	}
	reason := "devuelto a la bandeja desde la TUI"
	req.Reason = &reason
	return b.Reassign(ctx, req)
}

func (b liveBackend) Report(ctx context.Context, p api.GetCostReportParams) (api.CostReport, error) {
	r, err := b.c.GetCostReportWithResponse(ctx, &p)
	if err := apiErr(r, err, r != nil && r.JSON200 != nil, func() *api.Error { return r.JSONDefault }); err != nil {
		return api.CostReport{}, err
	}
	return *r.JSON200, nil
}

func (b liveBackend) Start(ctx context.Context, o devflow.StartOptions) (devflow.StartResult, error) {
	if b.flow == nil {
		return devflow.StartResult{}, errors.New("devflow no disponible")
	}
	return b.flow.Start(ctx, o)
}

func (b liveBackend) OpenPR(ctx context.Context, o devflow.PROptions) (devflow.PRResult, error) {
	if b.flow == nil {
		return devflow.PRResult{}, errors.New("devflow no disponible")
	}
	return b.flow.OpenPR(ctx, o)
}

// httpResp is satisfied by every generated *XxxResponse.
type httpResp interface{ StatusCode() int }

// apiErr normalises transport and API errors into readable messages.
func apiErr(r httpResp, err error, ok bool, def func() *api.Error) error {
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	code := r.StatusCode()
	if code == http.StatusUnauthorized {
		return auth.ErrNotLoggedIn
	}
	if e := def(); e != nil && e.Message != "" {
		return fmt.Errorf("API %d: %s", code, e.Message)
	}
	return fmt.Errorf("API: respuesta inesperada (%d)", code)
}

// errKind classifies errors for the global banner.
type errKind int

const (
	errOther errKind = iota
	errDaemonDown
	errLoggedOut
)

func classify(err error) errKind {
	switch {
	case err == nil:
		return errOther
	case errors.Is(err, localapi.ErrDaemonDown):
		return errDaemonDown
	case errors.Is(err, auth.ErrNotLoggedIn), strings.Contains(err.Error(), "not logged in"):
		return errLoggedOut
	}
	return errOther
}
