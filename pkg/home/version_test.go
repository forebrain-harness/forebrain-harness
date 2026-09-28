package home

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	tests := []struct {
		name   string
		linked string
		info   *debug.BuildInfo
		ok     bool
		want   string
	}{
		{
			name:   "link-time version wins over build info",
			linked: "v1.2.3",
			info:   &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}},
			ok:     true,
			want:   "v1.2.3",
		},
		{
			name:   "go install build reports the recorded module version",
			linked: "",
			info:   &debug.BuildInfo{Main: debug.Module{Version: "v0.4.0"}},
			ok:     true,
			want:   "v0.4.0",
		},
		{
			name:   "no build info falls back to devel",
			linked: "",
			info:   nil,
			ok:     false,
			want:   "(devel)",
		},
		{
			name:   "empty module version falls back to devel",
			linked: "",
			info:   &debug.BuildInfo{Main: debug.Module{Version: ""}},
			ok:     true,
			want:   "(devel)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.linked, tt.info, tt.ok); got != tt.want {
				t.Errorf("resolveVersion(%q, ...) = %q, want %q", tt.linked, got, tt.want)
			}
		})
	}
}
