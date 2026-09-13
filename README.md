# Orca

Orca 是一个用 Go 编写的 AI Agent 命令行工具：你给出一个目标，模型自主调用本地工具（读写文件、执行命令等）逐步完成，全过程自动保存为会话，可随时恢复、继续或删除。

```bash
$ orca "为 internal/llm/openai.go 生成单元测试文件"
```

## 特性

- **多 Provider**：支持兼容 OpenAI Chat Completions 协议的任意服务（Ollama 本地模型、DashScope 等），以及 otter Web 平台
- **内置工具**：read / write / edit / bash / create_task，模型按需调用，串行执行
- **双重工具协议**：支持原生 function calling 的 Provider 直接下发工具声明；不支持的 Provider 走文本命令协议（如 otter）
- **会话持久化**：每个任务自动保存消息与 token 统计，支持列表、删除与 `-session` 恢复
- **上下文注入**：`-f` 附加文件，`-s` 覆盖系统提示词
- **流式输出**：OpenAI 兼容层与服务端 SSE 流式对接

## 构建

要求 Go 1.26+。项目通过 `go.mod` 中的 `replace` 依赖同级的 [github.com/xiws/otter](../otter) 仓库，请保持两个仓库相邻存放。

```bash
make build      # 构建 ./orca
make publish    # 构建并安装到 $GOPATH/bin
```

或直接：

```bash
go build -o orca ./cmd/cli
```

## 快速开始

```bash
# 执行任务
orca "为 internal/llm/openai.go 生成单元测试文件"

# 指定 Provider 与模型（须成对使用）
orca -p otter -m chatgpt "这个项目做了什么"

# 附加文件作为上下文（可重复）
orca -f main.go -f README.md "解释这两个文件"

# 恢复之前的会话继续对话
orca -session 1234567890123456789 "继续完成上面的任务"

# 会话管理
orca session list
```

## 命令行用法

```text
Usage:
  orca [options] <prompt>                启动新任务
  orca [options] -session <id> <prompt>  恢复指定会话继续对话
  orca session <command>                 管理已保存的会话
```

### 选项

| 选项 | 说明 |
| --- | --- |
| `-p, --provider <name>` | 指定模型 Provider，须与 `-m` 一起使用；取值为 `models.json` 中的 provider key，如 `otter`、`ollama` |
| `-m, --model <id>` | 指定模型 ID，须与 `-p` 一起使用，如 `chatgpt`、`deepseek` |
| `-s, --sp <file>` | 用文件内容覆盖系统提示词 |
| `-f, --file <path>` | 附加文件到本次请求，可重复使用；文件以 `<file path="...">` 包裹后放入用户消息 |
| `-session <id>` | 恢复指定的 session id 继续对话 |
| `-h, --help` | 显示完整帮助 |

> `-p` / `-m` 缺省时使用 `setting.json` 中的默认 Provider 与模型。

### session 子命令

```text
orca session list              # 列出所有已保存的会话（别名 ls）
orca session rm <id>...        # 删除指定会话（别名 remove）
orca session rm --all          # 删除所有会话（别名 -a）
orca session help              # 显示 session 帮助
```

## 配置文件

配置来自两个层级，项目目录优先于用户主目录（同名文件做字段级合并，项目级生效）：

| 文件 | 位置 | 说明 |
| --- | --- | --- |
| `models.json` | `./.orca/models.json`、`~/.orca/models.json` | Provider 与模型定义 |
| `setting.json` | `./.orca/setting.json`、`~/.orca/setting.json` | 默认 Provider / 模型、调试开关 |
| `sessions/` | `./.orca/sessions/`、`~/.orca/sessions/` | 会话数据，每次任务自动写入 |

`models.json` 按 Provider 组织模型列表：

```json
{
  "providers": {
    "ollama": {
      "name": "Ollama (Local)",
      "api": "openai-completions",
      "baseUrl": "http://localhost:11434/v1",
      "apiKey": "ollama",
      "models": [
        {
          "id": "ornith:latest",
          "name": "ornith",
          "contextWindow": 65536,
          "supportsTools": true
        }
      ]
    }
  }
}
```

- `api`：协议类型，`openai-completions` 走 OpenAI 兼容接口（原生 function calling），`otter` 走 otter Web 平台
- `supportsTools`：为 `false` 时命令协议随系统提示词一起下发
- `contextWindow`：模型的上下文窗口，用于 token 用量展示

`setting.json`：

```json
{
  "defaultProvider": "otter",
  "defaultModel": "chatgpt",
  "debug": "false"
}
```

`debug` 为 `"true"` 时，每轮请求后输出本次与累计 token 消耗。

## 内置工具

模型在任务过程中可调用以下工具，全部限制在当前项目目录（workspace）内：

| 工具 | 说明 |
| --- | --- |
| `read` | 按行读取文件，单次最多 2000 行，返回带行号的文本 |
| `write` | 整体覆盖写入文件，自动创建父目录，原子写 |
| `edit` | 基于 unified diff 内容匹配的精准替换，多个片段顺序应用，任一失败则整体不落盘 |
| `bash` | 执行 shell 命令，默认超时 60s，stdout / stderr 合并返回（上限 32KB） |
| `create_task` | 将目标拆解为子任务，各自独立执行并汇总结果 |

## 开发

```bash
make run          # 运行示例任务
go test ./...     # 运行全部测试
```

更深入的设计文档见 [docs/](docs/)：命令协议（[command.md](docs/command.md)）、LLM 层（[llm.md](docs/llm.md)）、会话（[agent_session.md](docs/agent_session.md)）、事件（[event_handler.md](docs/event_handler.md)）等。
