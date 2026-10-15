package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/hiberus-industria/forge/internal/domain"
)

// Service implements the API use cases.
type Service struct {
	store  Store
	gh     GitHub
	prices PriceSource
	fx     FXSource
	org    string
	log    *slog.Logger
	now    func() time.Time

	mu        sync.Mutex
	priceBook *domain.PriceBook
	bookAt    time.Time
}

// Deps wires a Service. GitHub, PriceSource and FXSource may be nil (the
// corresponding features are then disabled), which keeps tests and local
// development simple.
type Deps struct {
	Store  Store
	GitHub GitHub
	Prices PriceSource
	FX     FXSource
	Org    string
	Logger *slog.Logger
	Now    func() time.Time
}

// New builds the Service.
func New(d Deps) *Service {
	s := &Service{store: d.Store, gh: d.GitHub, prices: d.Prices, fx: d.FX, org: strings.ToLower(d.Org), log: d.Logger, now: d.Now}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.org == "" && d.GitHub != nil {
		s.org = strings.ToLower(d.GitHub.Org())
	}
	return s
}

// Ping checks the store (readiness).
func (s *Service) Ping(ctx context.Context) error { return s.store.Ping(ctx) }

// Org is the tracked GitHub organization.
func (s *Service) Org() string { return s.org }

// canonRepo lowercases a repository reference: GitHub names are
// case-insensitive and daemons report whatever the git remote says.
func canonRepo(r domain.RepoRef) domain.RepoRef {
	return domain.RepoRef{Owner: strings.ToLower(r.Owner), Name: strings.ToLower(r.Name)}
}

func canonIssue(i domain.IssueRef) domain.IssueRef {
	return domain.IssueRef{Repo: canonRepo(i.Repo), Number: i.Number}
}

func canonAttribution(a domain.Attribution) domain.Attribution {
	if a.Repo != nil {
		r := canonRepo(*a.Repo)
		a.Repo = &r
	}
	if a.Issue != nil {
		i := canonIssue(*a.Issue)
		a.Issue = &i
		if a.Repo == nil {
			r := i.Repo
			a.Repo = &r
		}
	}
	return a
}

func (s *Service) inOrg(r domain.RepoRef) bool { return strings.EqualFold(r.Owner, s.org) }

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}
