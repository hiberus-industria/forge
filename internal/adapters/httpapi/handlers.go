package httpapi

import (
	"context"

	"github.com/hiberus-industria/forge/internal/api"
	"github.com/hiberus-industria/forge/internal/app"
	"github.com/hiberus-industria/forge/internal/domain"
)

// handlers implements api.StrictServerInterface on top of the use cases.
// Errors are returned as-is and mapped to HTTP by writeError.
type handlers struct {
	svc *app.Service
}

var _ api.StrictServerInterface = (*handlers)(nil)

func (h *handlers) GetMe(ctx context.Context, _ api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	me, err := h.svc.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	return api.GetMe200JSONResponse(toUser(me)), nil
}

func (h *handlers) LinkGitHub(ctx context.Context, req api.LinkGitHubRequestObject) (api.LinkGitHubResponseObject, error) {
	me, err := h.svc.LinkGitHub(ctx, deref(req.Body.GithubToken))
	if err != nil {
		return nil, err
	}
	return api.LinkGitHub200JSONResponse(toUser(me)), nil
}

func (h *handlers) ListUsers(ctx context.Context, _ api.ListUsersRequestObject) (api.ListUsersResponseObject, error) {
	us, err := h.svc.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	return api.ListUsers200JSONResponse(mapSlice(us, func(u app.User) api.User { return toUser(app.Me{User: u}) })), nil
}

func (h *handlers) IngestUsageEvents(ctx context.Context, req api.IngestUsageEventsRequestObject) (api.IngestUsageEventsResponseObject, error) {
	out := api.IngestResult{}
	var evs []domain.UsageEvent
	for _, in := range req.Body.Events {
		e, err := fromEventInput(in)
		if err != nil {
			out.Rejected = append(out.Rejected, rejection(in.ExternalId, err.Error()))
			continue
		}
		evs = append(evs, e)
	}
	res, err := h.svc.Ingest(ctx, evs)
	if err != nil {
		return nil, err
	}
	out.Accepted, out.Duplicates = res.Accepted, res.Duplicates
	for _, r := range res.Rejected {
		out.Rejected = append(out.Rejected, rejection(r.ExternalID, r.Reason))
	}
	if out.Rejected == nil {
		out.Rejected = rejectionList{}
	}
	return api.IngestUsageEvents200JSONResponse(out), nil
}

type rejectionList = []struct {
	ExternalId string `json:"external_id"`
	Reason     string `json:"reason"`
}

func rejection(id, reason string) struct {
	ExternalId string `json:"external_id"`
	Reason     string `json:"reason"`
} {
	return struct {
		ExternalId string `json:"external_id"`
		Reason     string `json:"reason"`
	}{id, reason}
}

func (h *handlers) ListUsageEvents(ctx context.Context, req api.ListUsageEventsRequestObject) (api.ListUsageEventsResponseObject, error) {
	p := req.Params
	page, err := h.svc.ListEvents(ctx, app.EventQuery{
		User: deref(p.User), Repo: deref(p.Repo), Issue: deref(p.Issue), Bucket: domain.Bucket(deref(p.Bucket)),
		SessionID: deref(p.SessionId), From: p.From, To: p.To, Cursor: deref(p.Cursor), Limit: deref(p.Limit),
	})
	if err != nil {
		return nil, err
	}
	return api.ListUsageEvents200JSONResponse(api.UsageEventPage{
		Items: mapSlice(page.Items, toEvent), NextCursor: optStr(page.NextCursor),
	}), nil
}

func (h *handlers) ReassignUsageEvents(ctx context.Context, req api.ReassignUsageEventsRequestObject) (api.ReassignUsageEventsResponseObject, error) {
	var to *domain.IssueRef
	if req.Body.To != nil {
		ref, err := fromIssueRef(*req.Body.To)
		if err != nil {
			return nil, err
		}
		to = &ref
	}
	res, err := h.svc.Reassign(ctx, req.Body.EventIds, to, deref(req.Body.Reason))
	if err != nil {
		return nil, err
	}
	return api.ReassignUsageEvents200JSONResponse(api.ReassignResult{Updated: res.Updated, Skipped: res.Skipped}), nil
}

func (h *handlers) RepriceUsageEvents(ctx context.Context, req api.RepriceUsageEventsRequestObject) (api.RepriceUsageEventsResponseObject, error) {
	n, err := h.svc.Reprice(ctx, req.Body.From, deref(req.Body.Model))
	if err != nil {
		return nil, err
	}
	return api.RepriceUsageEvents200JSONResponse(api.CountResult{Count: n}), nil
}

func (h *handlers) ListInbox(ctx context.Context, req api.ListInboxRequestObject) (api.ListInboxResponseObject, error) {
	p := req.Params
	gs, err := h.svc.Inbox(ctx, app.InboxQuery{User: deref(p.User), Repo: deref(p.Repo), From: p.From, To: p.To})
	if err != nil {
		return nil, err
	}
	return api.ListInbox200JSONResponse(mapSlice(gs, toInboxGroup)), nil
}

func (h *handlers) ListTasks(ctx context.Context, req api.ListTasksRequestObject) (api.ListTasksResponseObject, error) {
	p := req.Params
	var states []domain.TaskState
	for _, s := range deref(p.State) {
		states = append(states, domain.TaskState(s))
	}
	ts, err := h.svc.ListTasks(ctx, app.TaskQuery{Assignee: deref(p.Assignee), States: states, Repo: deref(p.Repo)})
	if err != nil {
		return nil, err
	}
	return api.ListTasks200JSONResponse(mapSlice(ts, toTask)), nil
}

func issueRef(owner, repo string, number int) domain.IssueRef {
	return domain.IssueRef{Repo: domain.RepoRef{Owner: owner, Name: repo}, Number: number}
}

func (h *handlers) GetTask(ctx context.Context, req api.GetTaskRequestObject) (api.GetTaskResponseObject, error) {
	d, err := h.svc.GetTask(ctx, issueRef(req.Owner, req.Repo, req.Number))
	if err != nil {
		return nil, err
	}
	return api.GetTask200JSONResponse(api.TaskDetail{
		Task: toTask(d.TaskView), ByModel: toRows(d.ByModel), ByUser: toRows(d.ByUser), ByDay: toRows(d.ByDay),
	}), nil
}

func (h *handlers) StartTask(ctx context.Context, req api.StartTaskRequestObject) (api.StartTaskResponseObject, error) {
	v, err := h.svc.StartTask(ctx, issueRef(req.Owner, req.Repo, req.Number), req.Body.Branch)
	if err != nil {
		return nil, err
	}
	return api.StartTask200JSONResponse(toTask(v)), nil
}

func (h *handlers) RecordTaskPR(ctx context.Context, req api.RecordTaskPRRequestObject) (api.RecordTaskPRResponseObject, error) {
	v, err := h.svc.RecordPR(ctx, issueRef(req.Owner, req.Repo, req.Number), req.Body.PrNumber)
	if err != nil {
		return nil, err
	}
	return api.RecordTaskPR200JSONResponse(toTask(v)), nil
}

func (h *handlers) ListRepos(ctx context.Context, _ api.ListReposRequestObject) (api.ListReposResponseObject, error) {
	rs, err := h.svc.ListRepos(ctx)
	if err != nil {
		return nil, err
	}
	return api.ListRepos200JSONResponse(mapSlice(rs, toRepo)), nil
}

func (h *handlers) GetRepo(ctx context.Context, req api.GetRepoRequestObject) (api.GetRepoResponseObject, error) {
	r, err := h.svc.GetRepo(ctx, domain.RepoRef{Owner: req.Owner, Name: req.Repo})
	if err != nil {
		return nil, err
	}
	return api.GetRepo200JSONResponse(toRepo(r)), nil
}

func (h *handlers) SetStatusMap(ctx context.Context, req api.SetStatusMapRequestObject) (api.SetStatusMapResponseObject, error) {
	b := req.Body
	opts := map[domain.TaskState]string{
		domain.TaskTodo: deref(b.Todo), domain.TaskInProgress: deref(b.InProgress),
		domain.TaskInReview: deref(b.InReview), domain.TaskDone: deref(b.Done),
	}
	r, err := h.svc.SetStatusMap(ctx, domain.RepoRef{Owner: req.Owner, Name: req.Repo}, opts)
	if err != nil {
		return nil, err
	}
	return api.SetStatusMap200JSONResponse(toRepo(r)), nil
}

func (h *handlers) GetCostReport(ctx context.Context, req api.GetCostReportRequestObject) (api.GetCostReportResponseObject, error) {
	p := req.Params
	rep, err := h.svc.CostReport(ctx, app.ReportQuery{
		GroupBy: app.GroupBy(p.GroupBy), Currency: domain.Currency(deref(p.Currency)), User: deref(p.User),
		Repo: deref(p.Repo), Bucket: domain.Bucket(deref(p.Bucket)), From: p.From, To: p.To,
	})
	if err != nil {
		return nil, err
	}
	return api.GetCostReport200JSONResponse(api.CostReport{
		Currency: api.Currency(rep.Currency), GroupBy: api.GroupBy(rep.GroupBy), From: rep.From, To: rep.To,
		Total: rep.Total, TotalUsdMicros: int64(rep.TotalUSD), UnpricedEvents: ptr(rep.Unpriced), Rows: toRows(rep.Rows),
	}), nil
}

func (h *handlers) ListPrices(ctx context.Context, req api.ListPricesRequestObject) (api.ListPricesResponseObject, error) {
	ps, err := h.svc.ListPrices(ctx, deref(req.Params.Model))
	if err != nil {
		return nil, err
	}
	return api.ListPrices200JSONResponse(mapSlice(ps, toPrice)), nil
}

func (h *handlers) CreatePrice(ctx context.Context, req api.CreatePriceRequestObject) (api.CreatePriceResponseObject, error) {
	b := req.Body
	p, err := h.svc.CreatePrice(ctx, domain.Price{
		Provider: deref(b.Provider), Model: b.Model, Input: b.Input, Output: b.Output,
		CacheRead: deref(b.CacheRead), CacheWrite: deref(b.CacheWrite), EffectiveFrom: b.EffectiveFrom,
	})
	if err != nil {
		return nil, err
	}
	return api.CreatePrice201JSONResponse(toPrice(p)), nil
}

func (h *handlers) SyncPrices(ctx context.Context, _ api.SyncPricesRequestObject) (api.SyncPricesResponseObject, error) {
	n, err := h.svc.SyncPrices(ctx)
	if err != nil {
		return nil, err
	}
	return api.SyncPrices200JSONResponse(api.CountResult{Count: n}), nil
}
