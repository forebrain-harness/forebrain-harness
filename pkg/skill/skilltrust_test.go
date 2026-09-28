package skill

import (
	"path/filepath"
	"testing"
)

func TestSourceForPathClassifiesSupportedRoots(t *testing.T) {
	home := t.TempDir()
	repo := t.TempDir()

	cases := []struct {
		name string
		path string
		want Source
	}{
		{
			name: "global",
			path: filepath.Join(home, "skills", "pptx", "SKILL.md"),
			want: SourceGlobal,
		},
		{
			name: "workspace",
			path: filepath.Join(home, "workspace", "skills", "repo-audit", "SKILL.md"),
			want: SourceWorkspace,
		},
		{
			name: "project",
			path: filepath.Join(repo, ".forebrain", "skills", "design-review", "SKILL.md"),
			want: SourceProject,
		},
		{
			name: "local",
			path: filepath.Join(repo, "tmp", "skill", "SKILL.md"),
			want: SourceLocal,
		},
	}

	for _, tc := range cases {
		if got := SourceForPath(home, repo, tc.path); got != tc.want {
			t.Fatalf("%s: source=%q want %q", tc.name, got, tc.want)
		}
	}
}
