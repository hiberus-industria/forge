package domain

import (
	"errors"
	"fmt"
	"time"
)

// TaskState is forge's lifecycle of an issue. It mirrors, but does not
// depend on, the Status field of each repository's GitHub Project.
type TaskState string

const (
	TaskTodo       TaskState = "todo"
	TaskInProgress TaskState = "in_progress"
	TaskInReview   TaskState = "in_review"
	TaskDone       TaskState = "done"
)

// Step is an action of the lifecycle path: start → pr → merge.
type Step string

const (
	StepStart Step = "start"
	StepPR    Step = "pr"
	StepMerge Step = "merge"
	StepClose Step = "close"  // closed without merge (won't do, duplicate...)
	StepReopen Step = "reopen"
)

var ErrInvalidTransition = errors.New("invalid lifecycle transition")

// Target returns the state reached by applying the step.
func (s Step) Target() TaskState {
	switch s {
	case StepStart, StepReopen:
		return TaskInProgress
	case StepPR:
		return TaskInReview
	default:
		return TaskDone
	}
}

// Task is an issue as seen by forge's lifecycle and cost tracking.
type Task struct {
	Ref       IssueRef
	Title     string
	State     TaskState
	Assignees []string
	Labels    []string
	URL       string

	StartedAt  *time.Time
	PROpenedAt *time.Time
	PRNumber   int
	ClosedAt   *time.Time

	// Snapshot is the cost frozen when the task was closed.
	Snapshot *CostSnapshot
}

// CostSnapshot freezes the cost of a task at close time. Later events
// (late syncs, post-merge work) still accrue; the difference is the
// adjustment shown to users.
type CostSnapshot struct {
	Cost   Micros
	Tokens Tokens
	At     time.Time
}

// Apply moves the task through the lifecycle. Steps are idempotent: applying
// a step whose target state has already been reached (or surpassed, for
// webhooks arriving out of order) is a no-op.
func (t *Task) Apply(step Step, at time.Time, current CostSnapshot) error {
	switch step {
	case StepStart:
		if t.State == TaskDone {
			return fmt.Errorf("%w: %s is closed, reopen it first", ErrInvalidTransition, t.Ref)
		}
		if t.StartedAt == nil {
			t.StartedAt = &at
		}
		if t.State == TaskTodo || t.State == "" {
			t.State = TaskInProgress
		}
	case StepPR:
		if t.State == TaskDone {
			return nil
		}
		if t.StartedAt == nil {
			t.StartedAt = &at
		}
		if t.PROpenedAt == nil {
			t.PROpenedAt = &at
		}
		t.State = TaskInReview
	case StepMerge, StepClose:
		if t.State == TaskDone {
			return nil
		}
		t.State = TaskDone
		t.ClosedAt = &at
		current.At = at
		t.Snapshot = &current
	case StepReopen:
		if t.State != TaskDone {
			return nil
		}
		t.State = TaskInProgress
		t.ClosedAt = nil
		t.Snapshot = nil
	default:
		return fmt.Errorf("%w: unknown step %q", ErrInvalidTransition, step)
	}
	return nil
}

// Adjustment is the cost accrued after the snapshot was taken.
func (t Task) Adjustment(current Micros) Micros {
	if t.Snapshot == nil {
		return 0
	}
	return current - t.Snapshot.Cost
}

// StatusMap maps lifecycle states to the option IDs of a repository
// Project's single-select Status field.
type StatusMap struct {
	ProjectID     string               `json:"project_id"`
	StatusFieldID string               `json:"status_field_id"`
	Options       map[TaskState]string `json:"options"`
	// Detected is false once an admin has overridden the automatic mapping.
	Detected bool `json:"detected"`
}

// StatusOption is a choice of a Project single-select Status field.
type StatusOption struct {
	ID   string
	Name string
}

// statusAliases lists the names recognised for each state, most specific first.
var statusAliases = map[TaskState][]string{
	TaskTodo:       {"todo", "to do", "ready", "backlog", "pendiente", "por hacer"},
	TaskInProgress: {"in progress", "doing", "wip", "en curso", "en progreso", "in development"},
	TaskInReview:   {"in review", "review", "code review", "en revision", "en revisión", "pr", "qa"},
	TaskDone:       {"done", "closed", "completed", "hecho", "terminado", "finalizado"},
}

// DetectStatusMap matches Project status options to lifecycle states by name.
// States without a match are left out; an admin fixes them from the TUI.
func DetectStatusMap(projectID, fieldID string, opts []StatusOption) StatusMap {
	sm := StatusMap{ProjectID: projectID, StatusFieldID: fieldID, Options: map[TaskState]string{}, Detected: true}
	for state, aliases := range statusAliases {
	outer:
		for _, alias := range aliases {
			for _, o := range opts {
				if normalizeStatus(o.Name) == alias {
					sm.Options[state] = o.ID
					break outer
				}
			}
		}
	}
	return sm
}

// Missing lists the lifecycle states without a mapped option.
func (sm StatusMap) Missing() []TaskState {
	var out []TaskState
	for _, s := range []TaskState{TaskTodo, TaskInProgress, TaskInReview, TaskDone} {
		if sm.Options[s] == "" {
			out = append(out, s)
		}
	}
	return out
}

func normalizeStatus(s string) string {
	// Drop emoji/punctuation commonly used in Project columns ("🏗 In progress").
	return Slugify(s, 0) // "in-progress"
}

func init() {
	// Store aliases in the same normalized form used for comparison.
	for st, aliases := range statusAliases {
		for i, a := range aliases {
			aliases[i] = Slugify(a, 0)
		}
		statusAliases[st] = aliases
	}
}
