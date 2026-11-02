package api

import "github.com/hexagon-codes/hexclaw/internal/externaltools"

// SetExternalTools 复用现有生成文件入口；文件归属仍来自原执行账本。
func (s *Server) SetExternalTools(service *externaltools.Service) { s.externalTools = service }
