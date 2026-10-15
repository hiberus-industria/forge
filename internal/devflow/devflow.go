// Package devflow implements forge's opinionated development path on the
// developer machine: start → pr (merge is observed by the API through
// GitHub webhooks). Git and GitHub actions run with the developer's own
// identity (local git + `gh auth token`); the forge API is then notified so
// it can move the Project status and keep the lifecycle.
package devflow

import (
	"context"

	"github.com/hiberus-industria/forge/internal/domain"
)

// StartOptions configures `forge start`.
type StartOptions struct {
	Issue domain.IssueRef
	// Type of branch; empty derives it from the issue labels.
	Type domain.BranchType
	// RepoPath is the local clone; empty means the current directory.
	RepoPath string
	// Progress, when set, receives human readable step updates
	// ("Creating branch feat/12-x", "Assigning issue"...).
	Progress func(step string)
}

// StartResult is the outcome of Start.
type StartResult struct {
	Branch string
	// Created is false when the branch already existed and was checked out.
	Created bool
	Title   string
}

// PROptions configures `forge pr`.
type PROptions struct {
	// Issue to link; nil derives it from the current branch.
	Issue    *domain.IssueRef
	RepoPath string
	Draft    bool
	// Title defaults to the issue title; Body gets "Closes #N" appended.
	Title    string
	Body     string
	Progress func(step string)
}

// PRResult is the outcome of OpenPR.
type PRResult struct {
	Number int
	URL    string
	// Existing is true when an open PR for the branch already existed.
	Existing bool
}

// Service runs the lifecycle steps. Used by the CLI commands and the TUI.
type Service interface {
	Start(ctx context.Context, opts StartOptions) (StartResult, error)
	OpenPR(ctx context.Context, opts PROptions) (PRResult, error)
}
