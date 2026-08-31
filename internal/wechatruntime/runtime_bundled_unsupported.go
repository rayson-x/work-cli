//go:build bundled_wechat && (!windows && !darwin || windows && !amd64 || darwin && !amd64 && !arm64)

package wechatruntime

func embeddedRuntime() ([]byte, string, bool) { return nil, "", false }
