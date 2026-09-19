package main

import (
	"fmt"
	"os"
	"strings"
	"unicode"
)

const sidecarCapabilityTokenEnv = "HEXCLAW_SIDECAR_CAPABILITY_TOKEN"
const desktopAPITokenEnv = "HEXCLAW_DESKTOP_API_TOKEN"

// desktopAPITokenFromEnv 只在受管桌面模式接收，并从后续子进程环境中移除。
func desktopAPITokenFromEnv(desktop bool) (string, error) {
	token := strings.TrimSpace(os.Getenv(desktopAPITokenEnv))
	if err := os.Unsetenv(desktopAPITokenEnv); err != nil {
		return "", err
	}
	if !desktop {
		return "", nil
	}
	if len(token) < 32 || len(token) > 512 {
		return "", fmt.Errorf("desktop API token must contain 32-512 bytes")
	}
	for _, r := range token {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", fmt.Errorf("desktop API token contains invalid characters")
		}
	}
	return token, nil
}

func sidecarCapabilityTokenFromEnv() (string, error) {
	token := strings.TrimSpace(os.Getenv(sidecarCapabilityTokenEnv))
	if token == "" {
		return "", nil
	}
	if len(token) < 32 || len(token) > 512 {
		return "", fmt.Errorf("%s must contain 32-512 bytes", sidecarCapabilityTokenEnv)
	}
	for _, r := range token {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", fmt.Errorf("%s contains invalid whitespace/control characters", sidecarCapabilityTokenEnv)
		}
	}
	return token, nil
}
