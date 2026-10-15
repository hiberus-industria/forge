package app

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hiberus-industria/forge/internal/domain"
)

func TestMergeStatusMap(t *testing.T) {
	p := &ProjectInfo{ID: "P", StatusFieldID: "F", Options: []domain.StatusOption{
		{ID: "a", Name: "Todo"}, {ID: "b", Name: "In Progress"}, {ID: "c", Name: "Review"}, {ID: "d", Name: "Done"}, {ID: "e", Name: "QA"},
	}}
	if sm := mergeStatusMap(nil, p); !sm.Detected || sm.Options[domain.TaskInReview] != "c" {
		t.Fatalf("detect: %+v", sm)
	}
	override := &domain.StatusMap{ProjectID: "P", Options: map[domain.TaskState]string{domain.TaskInReview: "e", domain.TaskDone: "gone"}}
	sm := mergeStatusMap(override, p)
	if sm.Detected || sm.Options[domain.TaskInReview] != "e" || sm.Options[domain.TaskDone] != "d" || sm.Options[domain.TaskTodo] != "" {
		t.Fatalf("override must be kept and stale options re-detected: %+v", sm)
	}
	override.ProjectID = "OTHER"
	if sm := mergeStatusMap(override, p); !sm.Detected {
		t.Fatal("override of another project must be dropped")
	}
}

func TestRateFor(t *testing.T) {
	d := func(n int) time.Time { return time.Date(2026, 1, n, 0, 0, 0, 0, time.UTC) }
	rates := []domain.FXRate{{Date: d(2), USDPerEUR: 1.1}, {Date: d(5), USDPerEUR: 1.2}}
	for day, want := range map[int]float64{1: 1.1, 2: 1.1, 4: 1.1, 5: 1.2, 9: 1.2} {
		if got := rateFor(rates, d(day)).USDPerEUR; got != want {
			t.Errorf("day %d: got %v want %v", day, got, want)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	c := EventCursor{OccurredAt: time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC), ID: uuid.New()}
	got, err := decodeCursor(encodeCursor(c))
	if err != nil || !got.OccurredAt.Equal(c.OccurredAt) || got.ID != c.ID {
		t.Fatalf("got %+v %v", got, err)
	}
	if _, err := decodeCursor("!!"); err == nil {
		t.Fatal("bad cursor accepted")
	}
}
