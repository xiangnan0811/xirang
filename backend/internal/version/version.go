package version

// 通过 ldflags 在编译时注入，例如：
// go build -ldflags "-X xirang/backend/internal/version.Version=0.1.0 ..."
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
)
