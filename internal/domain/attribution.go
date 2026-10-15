package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// RepoRef identifies a GitHub repository.
type RepoRef struct {
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

func (r RepoRef) String() string { return r.Owner + "/" + r.Name }

// Equal compares repositories case-insensitively, as GitHub does.
func (r RepoRef) Equal(o RepoRef) bool {
	return strings.EqualFold(r.Owner, o.Owner) && strings.EqualFold(r.Name, o.Name)
}

// ParseRepoRef parses "owner/name".
func ParseRepoRef(s string) (RepoRef, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(s), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return RepoRef{}, fmt.Errorf("invalid repository %q, want owner/name", s)
	}
	return RepoRef{Owner: owner, Name: name}, nil
}

// IssueRef identifies a GitHub issue, forge's unit of work ("task").
type IssueRef struct {
	Repo   RepoRef `json:"repo"`
	Number int     `json:"number"`
}

func (i IssueRef) String() string { return fmt.Sprintf("%s#%d", i.Repo, i.Number) }

// ParseIssueRef parses "owner/name#123".
func ParseIssueRef(s string) (IssueRef, error) {
	repo, num, ok := strings.Cut(s, "#")
	if !ok {
		return IssueRef{}, fmt.Errorf("invalid issue %q, want owner/name#number", s)
	}
	r, err := ParseRepoRef(repo)
	if err != nil {
		return IssueRef{}, err
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return IssueRef{}, fmt.Errorf("invalid issue number in %q", s)
	}
	return IssueRef{Repo: r, Number: n}, nil
}

// Bucket is where a usage event's cost lands.
type Bucket string

const (
	// BucketIssue: attributed to a concrete issue.
	BucketIssue Bucket = "issue"
	// BucketRepo: inside an organization repository but without an issue
	// (default branch, branch without number...). Shows up in the inbox.
	BucketRepo Bucket = "repo"
	// BucketExternal: outside any organization repository ("personal/other").
	// Shows up in the inbox; never discarded.
	BucketExternal Bucket = "external"
)

// Unassigned reports whether events in this bucket belong to the inbox.
func (b Bucket) Unassigned() bool { return b != BucketIssue }

// AttributionSource explains how an attribution was decided.
type AttributionSource string

const (
	SourceBranch     AttributionSource = "branch"     // parsed from the git branch name
	SourceManual     AttributionSource = "manual"     // forced by the developer (task override)
	SourceReassigned AttributionSource = "reassigned" // moved afterwards from the inbox or another issue
	SourceNone       AttributionSource = "none"       // could not be attributed
)

// Attribution links a usage event to the work it was spent on.
type Attribution struct {
	Bucket Bucket            `json:"bucket"`
	Source AttributionSource `json:"source"`
	// Repo is set for BucketIssue and BucketRepo.
	Repo *RepoRef `json:"repo,omitempty"`
	// Branch is the branch checked out when the request was made (org repos only).
	Branch string `json:"branch,omitempty"`
	// Issue is set for BucketIssue.
	Issue *IssueRef `json:"issue,omitempty"`
}

var ErrInconsistentAttribution = errors.New("inconsistent attribution")

// Validate checks that bucket and references agree.
func (a Attribution) Validate() error {
	switch a.Bucket {
	case BucketIssue:
		if a.Issue == nil || a.Issue.Number <= 0 {
			return fmt.Errorf("%w: issue bucket without issue", ErrInconsistentAttribution)
		}
	case BucketRepo:
		if a.Repo == nil || a.Issue != nil {
			return fmt.Errorf("%w: repo bucket needs a repo and no issue", ErrInconsistentAttribution)
		}
	case BucketExternal:
		if a.Repo != nil || a.Issue != nil || a.Branch != "" {
			return fmt.Errorf("%w: external bucket must not carry repository data", ErrInconsistentAttribution)
		}
	default:
		return fmt.Errorf("%w: unknown bucket %q", ErrInconsistentAttribution, a.Bucket)
	}
	return nil
}

// AttributionInput is what the developer machine knows when a request happens.
type AttributionInput struct {
	// Org is the GitHub organization forge tracks.
	Org string
	// Repo is the repository of the harness working directory, nil when the
	// directory is not a git repository with a GitHub remote.
	Repo *RepoRef
	// Branch is the checked-out branch at the time of the request.
	Branch string
	// Override is the task forced by the developer, if any.
	Override *IssueRef
}

// Attribute applies forge's attribution rules:
//
//  1. An explicit developer override always wins (SourceManual).
//  2. Outside an organization repository the event is external; repository
//     details are dropped for privacy.
//  3. A branch following the convention yields the issue (SourceBranch).
//  4. Otherwise the event stays on the repository, pending triage.
func Attribute(in AttributionInput) Attribution {
	if in.Override != nil {
		ov := *in.Override
		repo := ov.Repo
		a := Attribution{Bucket: BucketIssue, Source: SourceManual, Repo: &repo, Issue: &ov}
		if in.Repo != nil && in.Repo.Equal(ov.Repo) {
			a.Branch = in.Branch
		}
		return a
	}
	if in.Repo == nil || !strings.EqualFold(in.Repo.Owner, in.Org) {
		return Attribution{Bucket: BucketExternal, Source: SourceNone}
	}
	repo := *in.Repo
	if b, ok := ParseBranch(in.Branch); ok {
		return Attribution{
			Bucket: BucketIssue, Source: SourceBranch, Repo: &repo, Branch: in.Branch,
			Issue: &IssueRef{Repo: repo, Number: b.Issue},
		}
	}
	return Attribution{Bucket: BucketRepo, Source: SourceNone, Repo: &repo, Branch: in.Branch}
}

// Reassign returns the attribution of an event moved by a person to another
// issue (or back to the repository inbox when to is nil).
func (a Attribution) Reassign(to *IssueRef) Attribution {
	if to == nil {
		if a.Repo == nil {
			return Attribution{Bucket: BucketExternal, Source: SourceReassigned}
		}
		return Attribution{Bucket: BucketRepo, Source: SourceReassigned, Repo: a.Repo, Branch: a.Branch}
	}
	issue := *to
	repo := issue.Repo
	return Attribution{Bucket: BucketIssue, Source: SourceReassigned, Repo: &repo, Branch: a.Branch, Issue: &issue}
}

// BranchType is the conventional prefix of a work branch.
type BranchType string

const (
	BranchFeat     BranchType = "feat"
	BranchFix      BranchType = "fix"
	BranchChore    BranchType = "chore"
	BranchDocs     BranchType = "docs"
	BranchRefactor BranchType = "refactor"
	BranchTest     BranchType = "test"
	BranchSpike    BranchType = "spike"
)

// BranchTypes lists the accepted branch types in display order.
var BranchTypes = []BranchType{BranchFeat, BranchFix, BranchChore, BranchDocs, BranchRefactor, BranchTest, BranchSpike}

// Branch is a parsed work branch.
type Branch struct {
	Type  BranchType
	Issue int
	Slug  string
}

// branchRe matches the convention <type>/<issue>-<slug>. The type prefix is
// optional when parsing so that branches created by `gh issue develop`
// (<issue>-<slug>) are attributed too; forge always creates the full form.
var branchRe = regexp.MustCompile(`^(?:([a-z]+)/)?([1-9][0-9]*)(?:-([a-z0-9][a-z0-9._-]*))?$`)

// ParseBranch extracts the issue number from a branch name.
func ParseBranch(name string) (Branch, bool) {
	m := branchRe.FindStringSubmatch(strings.TrimPrefix(name, "refs/heads/"))
	if m == nil {
		return Branch{}, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return Branch{}, false
	}
	return Branch{Type: BranchType(m[1]), Issue: n, Slug: m[3]}, true
}

// BranchName builds the conventional branch name for an issue.
func BranchName(t BranchType, issue int, title string) string {
	if t == "" {
		t = BranchFeat
	}
	slug := Slugify(title, 40)
	if slug == "" {
		return fmt.Sprintf("%s/%d", t, issue)
	}
	return fmt.Sprintf("%s/%d-%s", t, issue, slug)
}

// BranchTypeFromLabels picks a branch type from the issue labels.
func BranchTypeFromLabels(labels []string) BranchType {
	for _, l := range labels {
		switch strings.ToLower(l) {
		case "bug", "fix", "type: bug", "kind/bug":
			return BranchFix
		case "documentation", "docs":
			return BranchDocs
		case "chore", "maintenance", "dependencies":
			return BranchChore
		case "refactor":
			return BranchRefactor
		case "spike", "research":
			return BranchSpike
		case "test", "tests":
			return BranchTest
		}
	}
	return BranchFeat
}

// Slugify lowercases, strips accents and joins words with dashes, cutting at
// a word boundary so that the result is at most max characters long.
func Slugify(s string, max int) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(unicode.ToLower(r))
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if max > 0 && len(out) > max {
		out = out[:max]
		if i := strings.LastIndexByte(out, '-'); i > max/2 {
			out = out[:i]
		}
		out = strings.Trim(out, "-")
	}
	return out
}
