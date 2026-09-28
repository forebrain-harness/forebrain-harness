package assembly

var SessionBootstrapMarkdownFiles = []string{
	"AGENTS.md",
	"SOUL.md",
	"USER.md",
}

func bootstrapMaxPerFile(maxBody int) int {
	n := maxBody / 10
	if n < 2048 {
		n = 2048
	}
	if n > 12000 {
		n = 12000
	}
	return n
}
