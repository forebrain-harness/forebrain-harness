package subscription

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// MaskID answers one shape per tier, and never lets the middle of the
// original show through: whatever the tier, the characters between the first
// four and the last four of the input are gone.
func TestMaskIDCoversEveryTierAndHidesTheMiddle(t *testing.T) {
	cases := []struct{ in, want string }{
		{strings.Repeat("a", 13) + "EFGH", "aaaa…EFGH"}, // >12: 4+…+4
		{strings.Repeat("b", 8) + "WXYZ", "…WXYZ"},      // 12: …+last 4
		{strings.Repeat("c", 5) + "QRST", "…QRST"},      // 9: …+last 4
		{strings.Repeat("d", 4) + "MNOP", "…"},          // 8: … only
		{"12345", "…"},                                  // 5: … only
		{"1234", "1234"},                                // 4: as-is
		{"", ""},                                        // 0: as-is
	}
	for _, c := range cases {
		got := MaskID(c.in)
		require.Equal(t, c.want, got, "MaskID(%q)", c.in)
		// The middle is what sits strictly between the first 4 and the last 4
		// characters; an input of 8 or fewer has none.
		if len(c.in) > 8 {
			middle := c.in[4 : len(c.in)-4]
			require.NotContains(t, got, middle, "MaskID(%q) leaked the middle", c.in)
		}
	}
}

// ErrExternalLogin is a sentinel the subscription implementations wrap with
// %w, so a surface tells an external source apart with errors.Is, never by
// matching the message.
func TestErrExternalLoginSurvivesWrapping(t *testing.T) {
	wrapped := fmt.Errorf("unset OPENCODE_API_KEY: %w", ErrExternalLogin)
	require.ErrorIs(t, wrapped, ErrExternalLogin)
	require.False(t, errors.Is(errors.New("other"), ErrExternalLogin))
}
