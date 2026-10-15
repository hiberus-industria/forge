package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/hiberus-industria/forge/internal/domain"
)

// User is a developer known to forge. Roles are not stored: they come from
// the access token on every request.
type User struct {
	ID          uuid.UUID
	Subject     string
	Email       string
	Name        string
	GitHubLogin string
}

// UserRef is the summary of a user embedded in other read models.
type UserRef struct {
	ID          uuid.UUID
	Name        string
	GitHubLogin string
}

// StoredEvent is a usage event as persisted, with its owner and price.
type StoredEvent struct {
	ID uuid.UUID
	domain.UsageEvent
	User       UserRef
	Cost       domain.Micros
	PriceID    int64
	Priced     bool
	ReceivedAt time.Time
}

// NewEvent is an event ready to be inserted.
type NewEvent struct {
	domain.UsageEvent
	UserID  uuid.UUID
	Cost    domain.Micros
	PriceID int64
	Priced  bool
}

// EventCursor is the keyset position for paging events newest first.
type EventCursor struct {
	OccurredAt time.Time
	ID         uuid.UUID
}

// EventFilter selects usage events.
type EventFilter struct {
	UserID    *uuid.UUID
	Repo      *domain.RepoRef
	Issue     int
	Bucket    domain.Bucket
	SessionID string
	Model     string
	From, To  *time.Time
	After     *EventCursor
	Limit     int
}

// Reassignment is one attribution change, written together with its audit row.
type Reassignment struct {
	EventID uuid.UUID
	From    domain.Attribution
	To      domain.Attribution
}

// CostUpdate is a recomputed event cost.
type CostUpdate struct {
	EventID uuid.UUID
	Cost    domain.Micros
	PriceID int64
	Priced  bool
}

// InboxFilter selects unassigned spend.
type InboxFilter struct {
	UserID   *uuid.UUID
	Repo     *domain.RepoRef
	From, To *time.Time
}

// InboxGroup is the unassigned spend of one session in one place.
type InboxGroup struct {
	Bucket    domain.Bucket
	Repo      *domain.RepoRef
	Branch    string
	Harness   domain.Harness
	SessionID string
	User      UserRef
	FirstAt   time.Time
	LastAt    time.Time
	Events    int
	Models    []string
	Cost      domain.Micros
	EventIDs  []uuid.UUID
}

// Repo is an organization repository with its Project configuration.
type Repo struct {
	RepoInfo
	Project   *ProjectInfo
	StatusMap *domain.StatusMap
	SyncedAt  *time.Time
}

// TaskRecord is a persisted issue with lifecycle data.
type TaskRecord struct {
	domain.Task
	NodeID      string
	GitHubState IssueState
	Branch      string
	// Placeholder is true when the issue is only known from usage events and
	// still has to be fetched from GitHub.
	Placeholder bool
}

// IssueInfo rebuilds the GitHub view of the task.
func (t TaskRecord) IssueInfo() IssueInfo {
	return IssueInfo{
		Ref: t.Ref, NodeID: t.NodeID, Title: t.Title, URL: t.URL, State: t.GitHubState,
		Assignees: t.Assignees, Labels: t.Labels,
	}
}

// TaskCost aggregates the spend of a task.
type TaskCost struct {
	Cost         domain.Micros
	Tokens       domain.Tokens
	Events       int
	LastActivity *time.Time
}

// TaskFilter selects tasks.
type TaskFilter struct {
	// Assignee is a GitHub login; empty means any.
	Assignee string
	// SpentBy restricts to tasks with usage events of this user.
	SpentBy *uuid.UUID
	States  []domain.TaskState
	Repo    *domain.RepoRef
}

// GroupBy is a cost report dimension.
type GroupBy string

const (
	GroupRepo    GroupBy = "repo"
	GroupIssue   GroupBy = "issue"
	GroupUser    GroupBy = "user"
	GroupModel   GroupBy = "model"
	GroupHarness GroupBy = "harness"
	GroupDay     GroupBy = "day"
)

// CostQuery selects and groups events for a report.
type CostQuery struct {
	GroupBy  GroupBy
	UserID   *uuid.UUID
	Repo     *domain.RepoRef
	Issue    *domain.IssueRef
	Bucket   domain.Bucket
	From, To *time.Time
}

// CostBucket is the cost of one group on one UTC day; the app converts each
// day with its FX rate and folds days into rows.
type CostBucket struct {
	Key      string
	Label    string
	Day      time.Time
	Cost     domain.Micros
	Tokens   domain.Tokens
	Events   int
	Unpriced int
}

// Store is the persistence port of the API.
type Store interface {
	Ping(ctx context.Context) error

	UpsertUser(ctx context.Context, subject, email, name string) (User, error)
	ListUsers(ctx context.Context) ([]User, error)
	UserByLogin(ctx context.Context, login string) (User, error)
	// SetGitHubLogin fails with ErrConflict when another user has the login.
	SetGitHubLogin(ctx context.Context, userID uuid.UUID, login string) (User, error)

	// InsertEvents inserts new events and ignores duplicates (same dedup key).
	InsertEvents(ctx context.Context, evs []NewEvent) (inserted int, err error)
	ListEvents(ctx context.Context, f EventFilter) ([]StoredEvent, error)
	EventsByIDs(ctx context.Context, ids []uuid.UUID) ([]StoredEvent, error)
	Reassign(ctx context.Context, actor uuid.UUID, reason string, changes []Reassignment) error
	UpdateCosts(ctx context.Context, updates []CostUpdate) error
	Inbox(ctx context.Context, f InboxFilter) ([]InboxGroup, error)

	ListRepos(ctx context.Context) ([]Repo, error)
	GetRepo(ctx context.Context, ref domain.RepoRef) (Repo, error)
	UpsertRepo(ctx context.Context, r Repo) error
	SaveStatusMap(ctx context.Context, ref domain.RepoRef, sm domain.StatusMap) error

	// EnsureIssues creates placeholder tasks for issues not known yet.
	EnsureIssues(ctx context.Context, refs []domain.IssueRef) error
	// UpsertIssue stores GitHub data without touching lifecycle fields.
	UpsertIssue(ctx context.Context, i IssueInfo) error
	GetTask(ctx context.Context, ref domain.IssueRef) (TaskRecord, error)
	ListTasks(ctx context.Context, f TaskFilter) ([]TaskRecord, error)
	Placeholders(ctx context.Context, limit int) ([]domain.IssueRef, error)
	// SaveLifecycle persists state, timestamps, PR, branch and snapshot.
	SaveLifecycle(ctx context.Context, t TaskRecord) error
	TaskCosts(ctx context.Context, refs []domain.IssueRef) (map[domain.IssueRef]TaskCost, error)

	CostBuckets(ctx context.Context, q CostQuery) ([]CostBucket, error)

	ListPrices(ctx context.Context, model string) ([]domain.Price, error)
	InsertPrice(ctx context.Context, p domain.Price, createdBy *uuid.UUID) (domain.Price, error)
	// UpsertSeedPrices inserts a seed price only when it differs from the
	// latest seed price of the same provider/model. Returns rows inserted.
	UpsertSeedPrices(ctx context.Context, ps []domain.Price) (int, error)

	UpsertFXRates(ctx context.Context, rates []domain.FXRate) error
	// FXRates returns rates from the latest one on or before since onwards.
	FXRates(ctx context.Context, since time.Time) ([]domain.FXRate, error)
	LatestFXDay(ctx context.Context) (time.Time, error)

	GetMeta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
}
