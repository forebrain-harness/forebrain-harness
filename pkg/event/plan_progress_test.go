package event

import "testing"

func TestPlanProgressOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		items     []PlanUpdateItem
		completed int
		total     int
		fallback  string
		want      PlanProgress
	}{
		{
			name:  "empty checklist reports zeros",
			items: nil,
			want:  PlanProgress{},
		},
		{
			name: "counters pass through",
			items: []PlanUpdateItem{
				{ID: "1", Content: "read the code", Status: "completed"},
				{ID: "2", Content: "write the fix", Status: "in_progress"},
			},
			completed: 1,
			total:     2,
			want:      PlanProgress{Done: 1, Total: 2, Active: "write the fix"},
		},
		{
			name: "shortest in-progress title wins, ties keep the first",
			items: []PlanUpdateItem{
				{ID: "1", Content: "a long winding task title", Status: "in_progress"},
				{ID: "2", Content: "short", Status: "in_progress"},
				{ID: "3", Content: "done thing", Status: "completed"},
			},
			fallback: "summary line",
			want:     PlanProgress{Active: "short"},
		},
		{
			name: "no in-progress item falls back to the payload summary",
			items: []PlanUpdateItem{
				{ID: "1", Content: "done thing", Status: "completed"},
			},
			fallback: "  summary line  ",
			want:     PlanProgress{Active: "summary line"},
		},
		{
			name:     "blank items and blank fallback yield empty active",
			items:    []PlanUpdateItem{{ID: "1", Status: "completed"}},
			fallback: "   ",
			want:     PlanProgress{},
		},
		{
			name:      "negative counters clamp to zero",
			completed: -3,
			total:     -1,
			want:      PlanProgress{},
		},
		{
			name: "in-progress item with blank content is skipped",
			items: []PlanUpdateItem{
				{ID: "1", Content: "   ", Status: "in_progress"},
				{ID: "2", Content: "real task", Status: "in_progress"},
			},
			want: PlanProgress{Active: "real task"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := PlanProgressOf(tt.items, tt.completed, tt.total, tt.fallback)
			if got != tt.want {
				t.Fatalf("PlanProgressOf = %+v, want %+v", got, tt.want)
			}
		})
	}
}
