//go:build !windows

package builtin

func platformShellCommand(command string, _ []string) (string, []string) {
	return "/bin/sh", []string{"-c", command}
}
