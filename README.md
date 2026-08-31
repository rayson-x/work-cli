# work-cli

[中文版](./README.zh.md) | [English](./README.md)

`work-cli` is one command-line entry point for agents working with product conversations and apparel progress.

Download the archive for your platform from [GitHub Releases](https://github.com/rayson-x/work-cli/releases), extract it, then run:

```text
work-cli --version
work-cli --help
```

## Commands

| Command | Use it to | Start with |
| --- | --- | --- |
| `work-cli wechat` | Read local WeChat conversations and export available files | `work-cli wechat --help` |
| `work-cli media` | Understand local images and videos | `work-cli media --help` |
| `work-cli image` | Generate or edit images and save the results locally | `work-cli image --help` |
| `work-cli track` | Read and update apparel progress records | `work-cli track --help` |

Every command has `--help`. Use the help shown by the installed command as the current source of its arguments and output.

## Examples

```text
work-cli wechat sessions
work-cli wechat history --help

work-cli media resolve <file>
work-cli media resolve-batch <image> <image>

work-cli image generate --prompt <text> --out-dir <directory>
work-cli image edit --image <image> --prompt <text> --out-dir <directory>

work-cli track query --help
work-cli track apply --help
work-cli track style-events --style-id <style-id>
```

If a command needs sign-in or a permission, it returns the next required action. Do not invent flags or substitute another executable; retry the same `work-cli` command after completing that action.
