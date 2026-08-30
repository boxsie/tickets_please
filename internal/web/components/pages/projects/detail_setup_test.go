package projects

import (
	"context"
	"strings"
	"testing"

	"tickets_please/internal/domain"
)

func TestDetailSuggestsCommitHookOnlyForEmptyProject(t *testing.T) {
	tests := []struct {
		name  string
		total int
		want  bool
	}{
		{name: "empty project", total: 0, want: true},
		{name: "existing project", total: 1, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rendered strings.Builder
			props := DetailProps{
				Project: &domain.Project{Slug: "demo", Name: "Demo"},
				Metrics: DashboardMetrics{Total: tt.total},
			}
			if err := Detail(props).Render(context.Background(), &rendered); err != nil {
				t.Fatalf("render detail: %v", err)
			}
			got := strings.Contains(rendered.String(), "tickets_please commit-hook install --repo .")
			if got != tt.want {
				t.Fatalf("commit-hook setup hint present = %v, want %v", got, tt.want)
			}
		})
	}
}
