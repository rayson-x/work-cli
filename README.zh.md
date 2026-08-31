# work-cli

[中文版](./README.zh.md) | [English](./README.md)

`work-cli` 是面向 Agent 的统一命令入口，用于处理产品沟通与款式进度。

请从 [GitHub Releases](https://github.com/rayson-x/work-cli/releases) 下载对应平台的压缩包，解压后执行：

```text
work-cli --version
work-cli --help
```

## 命令

| 命令 | 用途 | 从这里开始 |
| --- | --- | --- |
| `work-cli wechat` | 读取本机微信对话并导出可用文件 | `work-cli wechat --help` |
| `work-cli media` | 理解本地图片、视频与音频 | `work-cli media --help` |
| `work-cli image` | 生成或编辑图片，并保存到本地 | `work-cli image --help` |
| `work-cli track` | 查询和更新款式进度记录 | `work-cli track --help` |

每个命令都支持 `--help`。以已安装命令展示的帮助为准，获取当前参数和输出说明。

## 示例

```text
work-cli wechat sessions
work-cli wechat history --help

work-cli media resolve <file>
work-cli media transcribe <audio>
work-cli media resolve-batch <image> <image>

work-cli image generate --prompt <text> --out-dir <directory>
work-cli image edit --image <image> --prompt <text> --out-dir <directory>

work-cli track query --help
work-cli track apply --help
work-cli track style-events --style-id <style-id>
```

如果命令需要登录或权限，它会返回下一步操作。完成后仍使用同一条 `work-cli` 命令重试；不要自行替换为其他可执行文件或猜测参数。
