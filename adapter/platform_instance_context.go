package adapter

import "context"

type platformInstanceContextKey struct{}

// PlatformInstanceIdentity 标识实际接纳消息的物理账号，不从用户元数据推断。
type PlatformInstanceIdentity struct {
	ID       string `json:"platform_instance_id"`
	Platform string `json:"platform"`
}

// WithPlatformInstanceIdentity 由账号管理器在真实入站边界冻结成本归属。
func WithPlatformInstanceIdentity(ctx context.Context, id, platform string) context.Context {
	return context.WithValue(ctx, platformInstanceContextKey{}, PlatformInstanceIdentity{ID: id, Platform: platform})
}

// PlatformInstanceIdentityFromContext 仅返回真实入站边界写入的身份。
func PlatformInstanceIdentityFromContext(ctx context.Context) (PlatformInstanceIdentity, bool) {
	identity, ok := ctx.Value(platformInstanceContextKey{}).(PlatformInstanceIdentity)
	return identity, ok && identity.ID != "" && identity.Platform != ""
}
