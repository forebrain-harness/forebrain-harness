package assembly

var SessionBootstrapMarkdownFiles = []string{
	"AGENTS.md",
	"SOUL.md",
	"USER.md",
}

// PerFileRulesBudget is the per-file instruction budget the assembler applies
// when the merged rules body may hold maxBody runes: a tenth of the total,
// clamped to [2048, 12000]. Only a total of 120000 or more makes it 12000.
// Exported so the surfaces that warn about an oversized rule file warn with the
// same number the assembler cuts with.
func PerFileRulesBudget(maxBody int) int {
	n := maxBody / 10
	if n < 2048 {
		n = 2048
	}
	if n > 12000 {
		n = 12000
	}
	return n
}
