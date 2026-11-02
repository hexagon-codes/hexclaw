package hub

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestValidatePinnedMCPServer(t *testing.T) {
	npmIntegrity := "sha512-" + base64.StdEncoding.EncodeToString(make([]byte, 64))
	valid := McpServerMeta{
		Name:    "filesystem",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-filesystem@2026.7.10", "/tmp"},
		Status:  "pinned",
		Artifact: &MCPArtifact{
			Ecosystem:      "npm",
			Package:        "@modelcontextprotocol/server-filesystem",
			Version:        "2026.7.10",
			Integrity:      npmIntegrity,
			SourceRegistry: "https://registry.npmjs.org",
		},
	}

	tests := []struct {
		name    string
		mutate  func(*McpServerMeta)
		wantErr string
	}{
		{name: "valid exact npm artifact"},
		{name: "missing status", mutate: func(m *McpServerMeta) { m.Status = "" }, wantErr: "pinned artifact"},
		{name: "quarantined", mutate: func(m *McpServerMeta) { m.Status = "quarantined"; m.QuarantineReason = "package removed" }, wantErr: "已隔离"},
		{name: "argument does not bind version", mutate: func(m *McpServerMeta) { m.Args[1] = "@modelcontextprotocol/server-filesystem" }, wantErr: "未绑定 artifact"},
		{name: "extra npm package", mutate: func(m *McpServerMeta) { m.Args = append(m.Args, "--package=attacker@1.0.0") }, wantErr: "额外 package"},
		{name: "invalid integrity", mutate: func(m *McpServerMeta) { m.Artifact.Integrity = "sha512-not-base64" }, wantErr: "sha512 SRI"},
		{name: "alternate registry", mutate: func(m *McpServerMeta) { m.Artifact.SourceRegistry = "https://packages.example" }, wantErr: "source registry"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := valid
			meta.Args = append([]string(nil), valid.Args...)
			artifact := *valid.Artifact
			meta.Artifact = &artifact
			if tt.mutate != nil {
				tt.mutate(&meta)
			}
			got, err := ValidatePinnedMCPServer(meta)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got.Name() != meta.Name || strings.Join(got.Args(), "\x00") != strings.Join(meta.Args, "\x00") {
					t.Fatalf("validated projection drift: name=%q args=%v", got.Name(), got.Args())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestValidatePinnedMCPDependencyConstraint(t *testing.T) {
	const declared = "sdk-core[cli]>=1.12.0,<2"
	baseArgs := []string{"--from", "fixture-server==0.6.1", "python", "-m", "fixture_server"}
	with := func(value string) []string {
		return append([]string{"--with", value}, baseArgs...)
	}
	tests := []struct {
		name       string
		constraint string
		args       []string
		wantErr    bool
	}{
		{name: "legacy without declaration", args: baseArgs},
		{name: "declared named constraint", constraint: declared, args: with(declared)},
		{name: "undeclared", args: with(declared), wantErr: true},
		{name: "mismatched", constraint: declared, args: with("sdk-core[cli]>=2"), wantErr: true},
		{name: "duplicate", constraint: declared, args: append(with(declared), "--with", declared), wantErr: true},
		{name: "missing argument pair", constraint: declared, args: baseArgs, wantErr: true},
		{name: "direct URL", constraint: "sdk-core@https://example.invalid/sdk.whl", args: with("sdk-core@https://example.invalid/sdk.whl"), wantErr: true},
		{name: "local path", constraint: "/tmp/sdk-core", args: with("/tmp/sdk-core"), wantErr: true},
		{name: "missing version", constraint: "sdk-core", args: with("sdk-core"), wantErr: true},
		{name: "joined flag", constraint: declared, args: append([]string{"--with=" + declared}, baseArgs...), wantErr: true},
		{name: "extra index", constraint: declared, args: append(with(declared), "--index-url", "https://example.invalid"), wantErr: true},
		{name: "requirements file", constraint: declared, args: append(with(declared), "--requirements", "/tmp/requirements.txt"), wantErr: true},
		{name: "constraint does not replace main artifact", constraint: "fixture-server==0.6.1", args: []string{"--with", "fixture-server==0.6.1", "python", "-m", "fixture_server"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wire := map[string]any{
				"name": "fixture-constrained", "command": "uvx", "args": tt.args, "status": "pinned",
				"artifact": map[string]any{
					"ecosystem": "pypi", "package": "fixture-server", "version": "0.6.1",
					"integrity": "sha256:" + strings.Repeat("0", 64), "source_registry": "https://pypi.org",
					"dependency_constraint": tt.constraint,
				},
			}
			body, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			var meta McpServerMeta
			if decodeErr := json.Unmarshal(body, &meta); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			got, err := ValidatePinnedMCPServer(meta)
			if tt.wantErr {
				if err == nil {
					t.Fatal("unreviewed dependency configuration was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got.Args(), "\x00") != strings.Join(tt.args, "\x00") {
				t.Fatalf("argument projection drift: %v", got.Args())
			}
			artifactBody, err := json.Marshal(got.Artifact())
			if err != nil {
				t.Fatal(err)
			}
			var artifact map[string]any
			if err := json.Unmarshal(artifactBody, &artifact); err != nil {
				t.Fatal(err)
			}
			if tt.constraint != "" && artifact["dependency_constraint"] != tt.constraint {
				t.Fatalf("dependency metadata was not preserved: %s", artifactBody)
			}
		})
	}
}
