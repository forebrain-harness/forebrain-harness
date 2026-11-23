//go:build !unix

package safety

func sandboxSIGSYSExitCode() int {
	return -1
}
