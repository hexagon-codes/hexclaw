package browserruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type RuntimeManifest struct {
	SchemaVersion    int    `json:"schema_version"`
	Platform         string `json:"platform"`
	Arch             string `json:"arch"`
	Generation       string `json:"generation"`
	MinimumOSVersion string `json:"minimum_os_version"`
	Node             struct {
		Version string `json:"version"`
		Path    string `json:"path"`
	} `json:"node"`
	MCP struct {
		Package string `json:"package"`
		Version string `json:"version"`
		Entry   string `json:"entry"`
	} `json:"mcp"`
	Playwright struct {
		Version            string `json:"version"`
		BrowserRevision    string `json:"browser_revision"`
		ChromiumExecutable string `json:"chromium_executable"`
	} `json:"playwright"`
}

var ErrRuntimeUnsupported = errors.New("bundled browser runtime is not supported on this operating system version")

type LaunchOptions struct {
	ProfileDirectory              string
	OutputDirectory               string
	SocketDirectory               string
	Extension                     bool
	Headless                      bool
	UnrestrictedFiles             bool
	ViewportWidth, ViewportHeight int
}

type LaunchSpec struct {
	Command  string
	Args     []string
	Env      map[string]string
	Manifest RuntimeManifest
}

// LoadManifest 只加载随包的固定资源身份，不调用系统 Node 或在线安装器。
func LoadManifest(resourceDirectory string) (RuntimeManifest, string, error) {
	if resourceDirectory == "" {
		resourceDirectory = os.Getenv("HEXCLAW_RESOURCE_DIR")
	}
	if resourceDirectory == "" {
		return RuntimeManifest{}, "", errors.New("browser runtime resources are unavailable")
	}
	root, err := filepath.Abs(filepath.Join(resourceDirectory, "browser-runtime"))
	if err != nil {
		return RuntimeManifest{}, "", err
	}
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return RuntimeManifest{}, "", fmt.Errorf("read bundled browser runtime manifest: %w", err)
	}
	var manifest RuntimeManifest
	if err = json.Unmarshal(data, &manifest); err != nil {
		return RuntimeManifest{}, "", errors.New("bundled browser runtime manifest is invalid")
	}
	if manifest.SchemaVersion != 1 || manifest.Generation == "" || manifest.MCP.Package != "@playwright/mcp" || manifest.MCP.Version == "" || manifest.Node.Version == "" || manifest.Playwright.Version == "" || manifest.Playwright.BrowserRevision == "" {
		return RuntimeManifest{}, "", errors.New("bundled browser runtime identity is incomplete")
	}
	if manifest.Platform != runtime.GOOS || manifest.Arch != runtime.GOARCH {
		return RuntimeManifest{}, "", fmt.Errorf("bundled browser runtime platform %s/%s does not match %s/%s", manifest.Platform, manifest.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if runtime.GOOS == "darwin" {
		if manifest.MinimumOSVersion == "" {
			return RuntimeManifest{}, "", errors.New("bundled browser runtime minimum macOS version is missing")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		actual, versionErr := exec.CommandContext(ctx, "/usr/bin/sw_vers", "-productVersion").Output()
		cancel()
		if versionErr != nil {
			return RuntimeManifest{}, "", fmt.Errorf("browser runtime could not determine macOS version: %w", versionErr)
		}
		compatible, versionErr := versionAtLeast(strings.TrimSpace(string(actual)), manifest.MinimumOSVersion)
		if versionErr != nil {
			return RuntimeManifest{}, "", versionErr
		}
		if !compatible {
			return RuntimeManifest{}, "", fmt.Errorf("%w: requires macOS %s or later", ErrRuntimeUnsupported, manifest.MinimumOSVersion)
		}
	}
	return manifest, root, nil
}

func versionAtLeast(actual, minimum string) (bool, error) {
	parse := func(value string) ([]int, error) {
		parts := strings.Split(value, ".")
		result := make([]int, len(parts))
		for i, part := range parts {
			number, err := strconv.Atoi(part)
			if err != nil || number < 0 {
				return nil, errors.New("browser runtime operating system version is invalid")
			}
			result[i] = number
		}
		return result, nil
	}
	a, err := parse(actual)
	if err != nil {
		return false, err
	}
	m, err := parse(minimum)
	if err != nil {
		return false, err
	}
	for i := 0; i < len(a) || i < len(m); i++ {
		av, mv := 0, 0
		if i < len(a) {
			av = a[i]
		}
		if i < len(m) {
			mv = m[i]
		}
		if av != mv {
			return av > mv, nil
		}
	}
	return true, nil
}

func runtimePath(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", errors.New("bundled browser runtime paths must be relative to their generation")
	}
	clean := filepath.Clean(relative)
	if clean == ".." || len(clean) > 3 && clean[:3] == ".."+string(filepath.Separator) {
		return "", errors.New("bundled browser runtime path leaves its generation")
	}
	path := filepath.Join(root, clean)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("bundled browser runtime resource is unavailable: %w", err)
	}
	if info.IsDir() {
		return "", errors.New("bundled browser runtime entry points must be files")
	}
	return path, nil
}

// ManagedLaunch 生成原 MCP Manager 的启动规格；用户扩展浏览器不归应用终止。
func ManagedLaunch(resourceDirectory string, options LaunchOptions) (LaunchSpec, error) {
	manifest, root, err := LoadManifest(resourceDirectory)
	if err != nil {
		return LaunchSpec{}, err
	}
	node, err := runtimePath(root, manifest.Node.Path)
	if err != nil {
		return LaunchSpec{}, err
	}
	entry, err := runtimePath(root, manifest.MCP.Entry)
	if err != nil {
		return LaunchSpec{}, err
	}
	args := []string{entry, "--caps=vision", "--image-responses=allow"}
	if options.OutputDirectory != "" {
		args = append(args, "--output-dir", options.OutputDirectory)
	}
	if options.UnrestrictedFiles {
		args = append(args, "--allow-unrestricted-file-access")
	}
	if options.ViewportWidth > 0 && options.ViewportHeight > 0 {
		args = append(args, "--viewport-size", strconv.Itoa(options.ViewportWidth)+"x"+strconv.Itoa(options.ViewportHeight))
	}
	if options.Extension {
		args = append(args, "--extension")
	} else {
		browser, browserErr := runtimePath(root, manifest.Playwright.ChromiumExecutable)
		if browserErr != nil {
			return LaunchSpec{}, browserErr
		}
		if options.ProfileDirectory == "" {
			return LaunchSpec{}, errors.New("managed browser profile directory is required")
		}
		args = append(args, "--browser", "chromium", "--executable-path", browser, "--user-data-dir", options.ProfileDirectory)
		if options.Headless {
			args = append(args, "--headless")
		}
	}
	environment := map[string]string{"PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD": "1"}
	if options.SocketDirectory != "" {
		environment["PWTEST_SOCKETS_DIR"] = options.SocketDirectory
	}
	return LaunchSpec{Command: node, Args: args, Env: environment, Manifest: manifest}, nil
}
