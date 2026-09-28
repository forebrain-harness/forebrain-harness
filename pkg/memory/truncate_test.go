package memory

import "testing"

func TestMiddleTokens(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		budget int
		want   string
	}{
		{name: "under budget", input: "short output", budget: 100, want: "short output"},
		{name: "zero", input: "abcdef", budget: 0, want: "…2 tokens truncated…"},
		{name: "small budget", input: "example output", budget: 1, want: "ex…3 tokens truncated…ut"},
		{
			name:   "utf8",
			input:  "😀😀😀😀😀😀😀😀😀😀\nsecond line with text\n",
			budget: 8,
			want:   "😀😀😀😀…8 tokens truncated… line with text\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := MiddleTokens(test.input, test.budget); got != test.want {
				t.Fatalf("MiddleTokens() = %q, want %q", got, test.want)
			}
		})
	}
}
