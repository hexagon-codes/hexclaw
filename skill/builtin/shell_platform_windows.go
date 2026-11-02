package builtin

func platformShellCommand(command string, environment []string) (string, []string) {
	interpreter := shellEnvironmentValue(environment, "COMSPEC")
	if interpreter == "" {
		interpreter = "cmd.exe"
	}
	return interpreter, []string{"/d", "/s", "/c", command}
}
