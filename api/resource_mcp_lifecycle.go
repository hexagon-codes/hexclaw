package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/config"
	hexmcp "github.com/hexagon-codes/hexclaw/mcp"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/hub"
)

// MCPHub 与设置页共用 Hub 来源，类型门面不维护第二份目录。
func (s *Server) MCPHub() *hub.McpHub {
	if s.skillHub == nil {
		return nil
	}
	return hub.NewMcpHubFromHub(s.skillHub)
}

// handleMCPResourceStatus 补齐已保存 MCP 的启停 API，保持 false 的明确语义。
func (s *Server) handleMCPResourceStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil || req.Enabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "enabled must be provided as a boolean"})
		return
	}
	action := "disable"
	if *req.Enabled {
		action = "enable"
	}
	result, err := s.ManageMCP(r.Context(), action, r.PathValue("name"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, hexmcp.ErrServerChangeSuperseded) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result.Data)
}

type mcpResourceState struct {
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
	Persisted  bool   `json:"persisted"`
	Enabled    bool   `json:"enabled"`
	Connected  bool   `json:"connected"`
	ToolCount  int    `json:"tool_count"`
}

func (s *Server) mcpResourceState(name string) (mcpResourceState, error) {
	state := mcpResourceState{Name: name}
	if s.cfgWriter != nil {
		cfg, err := s.cfgWriter.GetMCPServer(name)
		if err != nil {
			return state, err
		}
		if cfg != nil {
			state.Configured = true
			state.Persisted = true
			state.Enabled = cfg.Enabled
		}
	}
	if !state.Configured && s.cfg != nil {
		s.cfgMu.RLock()
		for _, cfg := range s.cfg.MCP.Servers {
			if cfg.Name == name {
				state.Configured = true
				state.Enabled = cfg.Enabled
			}
		}
		s.cfgMu.RUnlock()
	}
	if s.mcpMgr != nil {
		for _, runtime := range s.mcpMgr.ServerStatuses() {
			if runtime.Name == name {
				state.Connected = runtime.Connected
				state.ToolCount = runtime.ToolCount
				if !state.Configured {
					state.Configured = true
					state.Enabled = true
				}
			}
		}
	}
	return state, nil
}

func mcpResourceResult(action string, state any) (*skill.Result, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return &skill.Result{Content: action + ": " + string(encoded), Data: state}, nil
}

func (s *Server) installMCPEntry(ctx context.Context, entry hub.ValidatedMCPServer) (mcpResourceState, error) {
	if s.mcpMgr == nil {
		return mcpResourceState{}, fmt.Errorf("MCP is disabled")
	}
	candidate := config.MCPServerConfig{Name: entry.Name(), Transport: "stdio", Command: entry.Command(), Args: entry.Args(), Env: entry.Env(), Enabled: true}
	connectCtx, cancel := context.WithTimeout(ctx, mcpAddImmediateConnectTimeout)
	defer cancel()
	_, err := s.mcpMgr.ConfigureServerBestEffort(connectCtx, entry.Name(), func() (*hexmcp.ServerConfig, error) {
		if s.cfgWriter != nil {
			previous, err := s.cfgWriter.GetMCPServer(entry.Name())
			if err != nil {
				return nil, err
			}
			if previous != nil {
				if previous.Command != candidate.Command || !reflect.DeepEqual(previous.Args, candidate.Args) || !reflect.DeepEqual(previous.Env, candidate.Env) {
					return nil, fmt.Errorf("MCP server already has different saved configuration")
				}
				return nil, nil
			}
			if err := s.cfgWriter.AppendMCPServer(candidate.Name, candidate.Transport, candidate.Command, candidate.Args, candidate.Env, ""); err != nil {
				return nil, err
			}
		} else {
			s.rememberMCPServerConfig(candidate)
		}
		return &hexmcp.ServerConfig{Name: candidate.Name, Transport: candidate.Transport, Command: candidate.Command, Args: candidate.Args, Env: candidate.Env, Enabled: true}, nil
	})
	if err != nil {
		return mcpResourceState{}, err
	}

	return s.mcpResourceState(entry.Name())
}

// ManageMCP 使用现有配置 Writer 和 Manager；输出只含实际生命周期状态，不带启动凭据。
func (s *Server) ManageMCP(ctx context.Context, action, name string) (*skill.Result, error) {
	if action == "catalog" {
		catalog := s.MCPHub()
		if catalog == nil {
			return nil, fmt.Errorf("MCP catalog is unavailable")
		}
		var entries []map[string]any
		for _, entry := range catalog.Search(name) {
			entries = append(entries, map[string]any{"name": entry.Name, "description": entry.Description, "status": entry.Status, "category": entry.Category, "config_hint": entry.ConfigHint})
		}
		return mcpResourceResult(action, map[string]any{"servers": entries, "total": len(entries)})
	}
	if s.mcpMgr == nil {
		return nil, fmt.Errorf("MCP is disabled")
	}
	if action == "list" {
		names := map[string]bool{}
		if s.cfg != nil {
			s.cfgMu.RLock()
			for _, cfg := range s.cfg.MCP.Servers {
				names[cfg.Name] = true
			}
			s.cfgMu.RUnlock()
		}
		for _, current := range s.mcpMgr.ConfiguredServerNames() {
			names[current] = true
		}
		var states []mcpResourceState
		for n := range names {
			state, err := s.mcpResourceState(n)
			if err != nil {
				return nil, err
			}
			states = append(states, state)
		}
		sort.Slice(states, func(i, j int) bool { return states[i].Name < states[j].Name })
		return mcpResourceResult(action, states)
	}
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("MCP server name is required")
	}
	if action == "install" {
		catalog := s.MCPHub()
		if catalog == nil {
			return nil, fmt.Errorf("MCP catalog is unavailable")
		}
		meta, err := catalog.Get(name)
		if err != nil {
			return nil, err
		}
		entry, err := hub.ValidatePinnedMCPServer(*meta)
		if err != nil {
			return nil, err
		}
		state, err := s.installMCPEntry(ctx, entry)
		if err != nil {
			return nil, err
		}
		return mcpResourceResult(action, state)
	}
	state, err := s.mcpResourceState(name)
	if err != nil {
		return nil, err
	}
	if !state.Configured {
		return nil, fmt.Errorf("MCP server is not configured")
	}
	switch action {
	case "get":
		return mcpResourceResult(action, state)
	case "refresh":
		if !state.Enabled {
			return nil, fmt.Errorf("MCP server is disabled")
		}
		refreshCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if err := s.mcpMgr.RestartServer(refreshCtx, name); err != nil {
			return nil, err
		}
	case "remove":
		_, err := s.mcpMgr.ConfigureServerBestEffort(ctx, name, func() (*hexmcp.ServerConfig, error) {
			if s.cfgWriter != nil {
				current, err := s.cfgWriter.GetMCPServer(name)
				if err != nil {
					return nil, err
				}
				if current != nil {
					if err := s.cfgWriter.RemoveMCPServer(name); err != nil {
						return nil, err
					}
				}
			} else if s.cfg != nil {
				s.cfgMu.Lock()
				var retained []config.MCPServerConfig
				for _, cfg := range s.cfg.MCP.Servers {
					if cfg.Name != name {
						retained = append(retained, cfg)
					}
				}
				s.cfg.MCP.Servers = retained
				s.cfgMu.Unlock()
			}
			return &hexmcp.ServerConfig{Name: name, Enabled: false}, nil
		})
		if err != nil {
			return nil, err
		}
	case "enable", "disable":
		if s.cfgWriter == nil {
			return nil, fmt.Errorf("MCP persistence is unavailable")
		}
		connectCtx, cancel := context.WithTimeout(ctx, mcpAddImmediateConnectTimeout)
		defer cancel()
		_, err := s.mcpMgr.ConfigureServerBestEffort(connectCtx, name, func() (*hexmcp.ServerConfig, error) {
			cfg, err := s.cfgWriter.GetMCPServer(name)
			if err != nil {
				return nil, err
			}
			if cfg == nil {
				return nil, fmt.Errorf("MCP server is not saved")
			}
			cfg.Enabled = action == "enable"
			if err := s.cfgWriter.UpsertMCPServer(*cfg); err != nil {
				return nil, err
			}
			return &hexmcp.ServerConfig{Name: name, Transport: cfg.Transport, Command: cfg.Command, Args: cfg.Args, Env: cfg.Env, Endpoint: cfg.Endpoint, Enabled: cfg.Enabled}, nil
		})
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported MCP lifecycle action")
	}
	state, err = s.mcpResourceState(name)
	if err != nil {
		return nil, err
	}
	return mcpResourceResult(action, state)
}
