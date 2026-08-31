//go:build bundled_wechat && windows && amd64

package wechatruntime

import _ "embed"

//go:embed assets/wechat-cli-windows-x64.exe
var bundledRuntime []byte

func embeddedRuntime() ([]byte, string, bool) { return bundledRuntime, "wechat-cli.exe", true }
