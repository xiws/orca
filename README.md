# Orca

一个 AI agent 执行框架的命令行工具。

Orca 让大型语言模型（LLM）理解你的需求，自主使用一组内置工具（读/写/编辑/执行命令）来完成你交给它的任务。它把整个工作过程拆解成带上下文的会话和任务，在多轮循环中一步步落地，直到满足需求。

Orca 基于 Go 编写，不依赖任何第三方库，直接与 OpenAI 兼容的接口通信。

## 特性

- **自主任务执行**：模型根据需求自行规划、拆解任务并落地。
- **内置工具**：`read` / `write` / `edit` / `bash` 四类基础操作。
- **多轮上下文循环**：每个任务的产出会作为上下文回流，驱动下一轮。
- **多级模型配置**：模型提供者与服务端通过 `models.json` 声明，项目级与用户级配置按优先级合并。
- **配置外置**：系统提示词、模型设置等通过 `.orca/` 目录管理，支持与当前项目、用户目录共存。

## 快速开始

需要 Go 1.25+。

```bash
# 构建
go build -o orca ./cmd/cli

# 运行（把需求写在提示词里，或从文件传入）
go run ./cmd/cli -f ./docs/test.md "实现文档中的需求"
```

首次运行会读取 `~/.orca/` 下的配置（`models.json` / `setting.json`）。
若不存在，Orca 会返回对应错误提示，你需要先完成配置。

## 命令行用法

```
orca -f ./docs/test.md -f ./README.md "这是 agent 信息说明"
```

| 参数 | 说明 |
| --- | --- |
| `-f, --file` | 用户要传入的附加文件，可重复使用。 |
| `-s, --sp` | 覆盖系统提示词。 |
| `-m, --model` | 指定模型。 |

### 配置文件

- `~/.orca/models.json` / `{project}/.orca/models.json`：模型提供方与模型信息。
- `~/.orca/setting.json` / `{project}/.orca/setting.json`：默认模型等设置。
- `~/.orca/system_prompt.md` / `{project}/.orca/system_prompt.md`：系统提示词。

**优先级**：项目级（`{project}/.orca/`）优先于用户级（`~/.orca/`）。

### 模型配置示例

`~/.orca/models.json` 中 `ollama` 节点的结构：

```jsonc
{
  "ollama": {
    "name": "Ollama (Local)",
    "api": "openai-completions",
    "baseUrl": "http://localhost:11434/v1",
    "apiKey": "ollama",
    "models": [
      { "id": "ornith:latest", "name": "ornith", "contextWindow": 65536, "supportsTools": true },
      { "id": "ornith-1.5:9b", "name": "ornith-1.5:9b", "contextWindow": 65536, "supportsTools": true }
    ]
  }
}
```

见 [`docs/llm.md`](./docs/llm.md) 了解模型对接细节。

## 工作原理

1. 解析 `-f` 传入的文件与提示词，创建任务，并初始化一场会话（携带当前项目路径、默认模型、系统提示词）。
2. 初始化运行时，注册事件与工具命令。
3. 把任务与会话交给运行时。
4. 运行时让模型找出上下文、给出目标，并拆解成子任务；prompt 由「总上下文 + 任务自身上下文」组成。
5. 模型以工具调用（tool call）形式产出操作，运行时按顺序同步执行。
6. 每个工具的产出结果回填进会话上下文，进入下一轮，直到模型不再产生新的工具调用。
7. 循环结束，汇总结果。

> 工具命令会**串行执行**，同文件多次读/写/编辑不会看到过期内容；后续 bash 命令可能依赖前面命令创建的文件。

## 架构

- **`cmd/cli`** —— 命令行入口与参数解析。
- **`internal/agent`** —— 核心：会话（`Session`）、任务（`Task`）、运行时（`Runtime`）与设置（`Settings`）；驱动多轮循环与工具调用。
- **`internal/llm`** —— 模型对接层；把模型输出组装成结构化结果，`openai.go` 对接 OpenAI 兼容接口（SSE 流式）。
- **`internal/handler`** —— 工具命令实现与应用层协议（read / write / edit / bash）。
- **`pkg/command`** —— 命令注册与分发。
- **`pkg/event`** —— 异步内存事件总线。
- **`pkg/utils`** —— 环境、路径、配置加载等实用工具。
- **`internal/assets`** —— 内置资源：系统提示词。

## 内置工具

模型可调用四类工具，输出被解析为结构化结果并执行：

| 工具 | 语义 | 说明 |
| --- | --- | --- |
| `read` | 只读 | 按行返回文件内容，不改动文件 |
| `write` | 覆盖重建 | 整个文件重写，未指定的内容全部丢弃；文件不存在则创建（含父目录） |
| `edit` | 精准修改 | 基于 `old_string` / `new_string` 做字符串替换，只改动命中部分 |
| `bash` | 执行 | 在指定目录下执行 shell 命令，返回 stdout / stderr / 退出码 |

`update` / `delete` 等基于文件语义的操作通过 `edit` / `write` 表达。修改已有文件优先用 `edit`，新建或整体重写才用 `write`。

见 [`docs/command.md`](./docs/command.md) 了解工具命令的协议与约定。

## 开发

```bash
go test ./...          # 运行测试
go vet ./...           # 静态检查
```

核心逻辑建议阅读：

- [`internal/agent/agent_runtime.go`](./internal/agent/agent_runtime.go)：命令执行与循环驱动。
- [`internal/handler/`](./internal/handler/)：工具命令与各协议解析。
- [`internal/llm/openai.go`](./internal/llm/openai.go)：OpenAI 兼容接口实现。

## 路线图

- [ ] 会话层的多轮循环：把工具结果回填成下一轮的 prompt。
- [ ] write / edit 的安全确认策略（哪些路径需要用户批准后才能写）。
