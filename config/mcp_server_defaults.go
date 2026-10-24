package config

import "gopkg.in/yaml.v3"

// UnmarshalYAML 兼容旧配置缺省启用，同时保留已明确保存的 false。
func (server *MCPServerConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain MCPServerConfig
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]any
	if err := node.Decode(&fields); err != nil {
		return err
	}
	if _, specified := fields["enabled"]; !specified && (decoded.Command != "" || decoded.Endpoint != "") {
		decoded.Enabled = true
	}
	*server = MCPServerConfig(decoded)
	return nil
}
