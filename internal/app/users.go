package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Me is the caller with its roles.
type Me struct {
	User
	Admin bool
}

// CurrentUser upserts and returns the caller (first call creates the user).
func (s *Service) CurrentUser(ctx context.Context) (Me, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok || p.Subject == "" {
		return Me{}, ErrUnauthorized
	}
	u, err := s.store.UpsertUser(ctx, p.Subject, p.Email, p.Name)
	if err != nil {
		return Me{}, err
	}
	return Me{User: u, Admin: p.Admin}, nil
}

// ListUsers returns every user (everything is visible to every member).
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	if _, err := s.CurrentUser(ctx); err != nil {
		return nil, err
	}
	return s.store.ListUsers(ctx)
}

// LinkGitHub resolves the login owning a developer's gh token, checks that
// it belongs to the organization and stores it. The token is not persisted.
func (s *Service) LinkGitHub(ctx context.Context, userToken string) (Me, error) {
	me, err := s.CurrentUser(ctx)
	if err != nil {
		return Me{}, err
	}
	if strings.TrimSpace(userToken) == "" {
		return Me{}, invalid("github_token is required")
	}
	if s.gh == nil {
		return Me{}, ErrNoGitHub
	}
	login, err := s.gh.UserFromToken(ctx, userToken)
	if err != nil {
		return Me{}, fmt.Errorf("%w: github token rejected: %w", ErrInvalid, err)
	}
	member, err := s.gh.IsOrgMember(ctx, login)
	if err != nil {
		return Me{}, err
	}
	if !member {
		return Me{}, fmt.Errorf("%w: %s is not a member of %s", ErrForbidden, login, s.org)
	}
	u, err := s.store.SetGitHubLogin(ctx, me.ID, login)
	if err != nil {
		return Me{}, err
	}
	return Me{User: u, Admin: me.Admin}, nil
}

// resolveUser turns a user filter ("me", a GitHub login, empty) into an id.
// Unknown logins yield ErrNotFound.
func (s *Service) resolveUser(ctx context.Context, me Me, filter string) (*uuid.UUID, error) {
	switch strings.TrimSpace(filter) {
	case "", "all":
		return nil, nil
	case "me":
		id := me.ID
		return &id, nil
	default:
		u, err := s.store.UserByLogin(ctx, filter)
		if err != nil {
			return nil, err
		}
		return &u.ID, nil
	}
}
