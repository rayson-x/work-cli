//go:build bundled_wechat && darwin && arm64

package wechatruntime

import _ "embed"

//go:embed assets/wechat-cli-macos-arm64
var bundledRuntime []byte

func embeddedRuntime() ([]byte, string, bool) { return bundledRuntime, "wechat-cli", true }
