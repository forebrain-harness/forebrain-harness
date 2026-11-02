// The git work-tree classifier's contract (plan 198 §4 step 1): every rule in
// the table has a command that hits it and a near-miss beside it that must not,
// and the wrappers a shell command hides behind are unwrapped.
package safety

import "testing"

func TestGitSweepRulesHit(t *testing.T) {
	cases := []struct {
		command string
		rule    string
	}{
		{"git stash", GitSweepStash},
		{"git stash push -m wip", GitSweepStash},
		{"git stash pop", GitSweepStash},
		{"git stash drop", GitSweepStash},
		{"git checkout -- .", GitSweepCheckoutPaths},
		{"git checkout -- pkg/tool/x.go", GitSweepCheckoutPaths},
		{"git checkout .", GitSweepCheckoutPaths},
		{"git checkout -f main", GitSweepCheckoutForce},
		{"git checkout --force main", GitSweepCheckoutForce},
		{"git restore pkg/tool/x.go", GitSweepRestore},
		{"git restore --staged .", GitSweepRestore},
		{"git reset --hard", GitSweepResetHard},
		{"git reset --hard HEAD~1", GitSweepResetHard},
		{"git reset --merge", GitSweepResetHard},
		{"git reset --keep HEAD", GitSweepResetHard},
		{"git clean -f", GitSweepClean},
		{"git clean -fd", GitSweepClean},
		{"git clean --force -d", GitSweepClean},
		{"git commit -a -m wip", GitSweepCommitAll},
		{"git commit -am wip", GitSweepCommitAll},
		{"git commit --all -m wip", GitSweepCommitAll},
		{"git add -A", GitSweepAddAll},
		{"git add --all", GitSweepAddAll},
		{"git add .", GitSweepAddAll},
		{"git add -u", GitSweepAddAll},
		{"git add --update", GitSweepAddAll},
		{"git switch --discard-changes main", GitSweepSwitchDiscard},
		{"git switch -f main", GitSweepSwitchDiscard},
		{"git switch --force main", GitSweepSwitchDiscard},
	}
	for _, tc := range cases {
		rule, ok := GitSweepsWorkTree(tc.command)
		if !ok {
			t.Errorf("%q must sweep the work tree under rule %q", tc.command, tc.rule)
			continue
		}
		if rule != tc.rule {
			t.Errorf("%q matched rule %q, want %q", tc.command, rule, tc.rule)
		}
	}
}

func TestGitSweepRulesNearMisses(t *testing.T) {
	cases := []string{
		"git stash list",
		"git stash show",
		"git stash show -p",
		"git checkout main",
		"git checkout -b feature",
		"git reset --soft HEAD~1",
		"git reset",
		"git clean -n",
		"git clean --dry-run",
		"git commit -m x",
		"git commit --amend -m x",
		"git add file.go",
		"git add -p",
		"git switch dev",
		"git switch -c feature",
		"git status",
		"git status --short",
		"git diff --stat",
		"git log --oneline -5",
		"git push origin dev",
		"git -C sub status",
		"git -c core.pager=cat status",
		"go test ./pkg/safety",
		"go build ./...",
		"make build",
		"",
	}
	for _, command := range cases {
		if rule, ok := GitSweepsWorkTree(command); ok {
			t.Errorf("%q must not sweep the work tree, matched rule %q", command, rule)
		}
	}
}

// The wrappers are the point: a git call that hides behind sudo, env or a
// shell's -c operand is the same git call, and the classifier has to reach it.
func TestGitSweepUnwrapsWrappers(t *testing.T) {
	cases := []struct {
		command string
		rule    string
	}{
		{"sudo git stash", GitSweepStash},
		{"sh -c 'git clean -fd'", GitSweepClean},
		{"bash -c \"git reset --hard HEAD~1\"", GitSweepResetHard},
		{"env A=1 git restore .", GitSweepRestore},
		{"env -i git checkout -- .", GitSweepCheckoutPaths},
		{"git -C sub stash", GitSweepStash},
		{"git -c core.pager=cat clean -fd", GitSweepClean},
		{"git --no-pager stash", GitSweepStash},
		{"sudo sh -c 'git add -A'", GitSweepAddAll},
		{"cd pkg && git stash", GitSweepStash},
		{"git status && git stash", GitSweepStash},
	}
	for _, tc := range cases {
		rule, ok := GitSweepsWorkTree(tc.command)
		if !ok {
			t.Errorf("%q must sweep the work tree under rule %q", tc.command, tc.rule)
			continue
		}
		if rule != tc.rule {
			t.Errorf("%q matched rule %q, want %q", tc.command, rule, tc.rule)
		}
	}
}

// A wrapper around something that only reads is still something that only
// reads: unwrapping must not turn every `sh -c` into a hit.
func TestGitSweepWrapperNearMisses(t *testing.T) {
	cases := []string{
		"sudo git status",
		"sh -c 'git log --oneline'",
		"env A=1 git diff",
		"sudo rm -rf /tmp/x",
		"sh -c 'go test ./...'",
	}
	for _, command := range cases {
		if rule, ok := GitSweepsWorkTree(command); ok {
			t.Errorf("%q must not sweep the work tree, matched rule %q", command, rule)
		}
	}
}
