//go:build !bundled_wechat

package wechatruntime

func embeddedRuntime() ([]byte, string, bool) { return nil, "", false }
