# Orca

Orca 是一个用 Go 编写的持久化 AI Agent 命令行工具。你给出一个目标，模型自主调用本地工具（读写文件、执行命令等）逐步完成；全过程以 durable run 模型保存，可随时恢复、取消、重试或继续。

```bash
$ orca "为 internal/llm/openai.go 生成单元测试文件"
```

## 核心概念

| 概念 | 说明 |
| --- | --- |
| **Session** | 用户交互时间线，包含工作区和标题 |
| **Task** | 版本化的业务目标，包含约束和验收标准 |
| **Run** | 任务版本的一次执行，包含工作流节点、策略与预算 |
| **Invocation** | 单次角色执行，拥有私有线程、上下文版本与游标 |

## 特性

- **多 Provider**：支持兼容 OpenAI Chat Completions 协议的任意服务（Ollama 本地模型、DashScope 等），以及 otter Web 平台
- **工作流模式**：ask / code / plan / agent / review / test / terminal / deliberate 八种模式，按任务性质选择
- **内置工具**：read / write / edit / bash / create_task / request_input，模型按需调用，串行执行
- **双重工具协议**：支持原生 function calling 的 Provider 直接下发工具声明；不支持的 Provider 走文本命令协议（如 otter）
- **持久化执行**：SQLite WAL 模式存储，CAS 版本控制，崩溃恢复，append-only 事件流
- **Run 生命周期**：排队 → 运行 → 等待输入/审批 → 完成/失败/取消/中断，均可跨进程恢复
- **上下文注入**：`-f` 附加文件，`-s` 覆盖系统提示词
- **流式输出**：实时 delta 事件推送，`run show --after` 可重放持久化事件
- **TUI 界面**：基于 Bubble Tea 的交互式终端界面

## 构建

要求 Go 1.26+。项目通过 `go.mod` 中的 `replace` 依赖同级的 [github.com/xiws/otter](../otter) 仓库，请保持两个仓库相邻存放。

```bash
make build          # 构建 ./orca
make build-tui      # 构建 ./orca-tui
make publish        # 构建并安装到 $GOPATH/bin
```

或直接：

```bash
go build -o orca ./cmd/cli
go build -o orca-tui ./cmd/tui
```

## 快速开始

```bash
# 执行任务
orca "为 internal/llm/openai.go 生成单元测试文件"

# 指定工作流模式
orca -mode code "重构 internal/agent 包的错误处理"

# 指定 Provider 与模型（须成对使用）
orca -p otter -m chatgpt "这个项目做了什么"

# 附加文件作为上下文（可重复）
orca -f main.go -f README.md "解释这两个文件"

# 继续在同一个 session 中提交新任务
orca -session 1234567890123456789 "接下来优化性能"

# 查看与管理运行
orca run list                           # 列出所有 run
orca run show 12345                     # 重放 run 的全部事件
orca run resume 12345                   # 恢复中断的 run
orca run respond 67890 "用户输入内容"     # 回复等待中的输入请求
orca run approve 67890                  # 审批通过
orca run cancel 12345                   # 取消运行

# 会话管理
orca session list                       # 列出所有会话
orca session show 12345                 # 查看会话详情
orca session rm 12345                   # 删除会话
```

## 命令行用法

```text
Usage:
  orca [options] PROMPT                      启动新任务
  orca -session ID [options] PROMPT          在同一会话中提交新任务
  orca run <command>                         管理运行
  orca task revise ID INPUT                  创建任务修订版本
  orca session <command>                     管理会话
```

### 选项

| 选项 | 说明 |
| --- | --- |
| `-p, --provider <name>` | 指定模型 Provider，须与 `-m` 一起使用；取值为 `models.json` 中的 provider key，如 `otter`、`ollama` |
| `-m, --model <id>` | 指定模型 ID，须与 `-p` 一起使用，如 `chatgpt`、`deepseek` |
| `-mode <name>` | 工作流模式：ask / code / plan / agent / review / test / terminal / deliberate |
| `-s, --sp <file>` | 用文件内容覆盖系统提示词 |
| `-f, --file <path>` | 附加文件到本次请求，可重复使用；文件以 `<file path="...">` 包裹后放入用户消息 |
| `-session <id>` | 在同一会话中继续提交新任务 |
| `-h, --help` | 显示完整帮助 |

> `-p` / `-m` 缺省时使用 `setting.json` 中的默认 Provider 与模型。

### run 子命令

```text
orca run list                              # 列出所有 run（别名 ls）
orca run show ID [--after SEQUENCE]        # 重放持久化事件（含子 run）
orca run resume ID                         # 恢复中断的 run
orca run retry TASK_ID                     # 重试任务
orca run respond INPUT_ID TEXT             # 回复等待中的输入
orca run approve INPUT_ID                  # 审批通过
orca run reject INPUT_ID                   # 审批拒绝
orca run cancel ID                         # 取消运行
orca run reconcile ID --note TEXT          # 手动确认 unknown 结果，标记失败，绝不重放
```

> 等待输入/审批的 run 会安全保存并正常退出，使用对应的 INPUT_ID 回复即可。

### session 子命令

```text
orca session list                          # 列出所有会话（别名 ls）
orca session show ID                       # 查看会话详情
orca session import PATH --owner WORKSPACE # 导入旧版 JSON 会话
orca session rm ID...                      # 删除指定会话（别名 remove / delete）
orca session rm --all                      # 删除所有会话（别名 -a）
```

### task 子命令

```text
orca task revise ID INPUT                  # 创建任务修订版本（不执行）
```

## 配置文件

配置来自两个层级，项目目录优先于用户主目录（同名文件做字段级合并，项目级生效）：

| 文件 | 位置 | 说明 |
| --- | --- | --- |
| `models.json` | `./.orca/models.json`、`~/.orca/models.json` | Provider 与模型定义 |
| `setting.json` | `./.orca/setting.json`、`~/.orca/setting.json` | 默认 Provider / 模型 |
| `system_prompt.md` | `./.orca/system_prompt.md`、`~/.orca/system_prompt.md` | 系统提示词模板 |
| `otter_prompt.md` | `./.orca/otter_prompt.md`、`~/.orca/otter_prompt.md` | otter 专用提示词 |
| `state.sqlite3` | `./.orca/state.sqlite3` | SQLite 持久化存储 |

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
- `apiKey`：支持 `${ENV_VAR}` 语法引用环境变量

`setting.json`：

```json
{
  "defaultProvider": "otter",
  "defaultModel": "chatgpt"
}
```

## 架构

```
cmd/cli          CLI 入口：参数解析、事件流展示
cmd/tui          TUI 入口：Bubble Tea 交互界面
internal/
  bootstrap      依赖装配：config → store → tools → providers → agent → workflow → app
  config         配置加载与合并，环境变量展开
  domain         核心领域类型：Session / Task / Run / Invocation / Event / Mutation
  store          SQLite 持久化层：WAL 模式、CAS 版本、崩溃恢复、旧数据导入
  model          模型协议：Client 接口、Message / Call / Tool / Cursor
  providers      Provider 实现：OpenAI 兼容、otter
  tools          工具网关：策略、审批、幂等重放、工作区隔离
  agent          执行器：检查点驱动的 Invocation 循环
  workflow       工作流编排：节点推进、委派、验证、有限修复
  app            服务层：调度、发布订阅、后台 Worker
```

### 存储层

SQLite WAL/FULL 模式，短事务，CAS 并发控制。所有写入通过 `Mutation` 原子提交，支持崩溃恢复（`Store.Recover`）与旧版 JSON 会话导入（`Store.Import`）。Schema 当前为 v2。

### 执行模型

Run 形成树结构（`RootRunID` / `ParentRunID` / `Depth`），WorkflowRunner 编排整棵树，AgentRunner 处理单次 Invocation。ToolGateway 作为工具执行的唯一边界，负责策略检查、审批流程与幂等重放。

## 内置工具

模型在任务过程中可调用以下工具，全部限制在当前项目目录（workspace）内：

| 工具 | 说明 |
| --- | --- |
| `read` | 按行读取文件，单次最大 1MiB / 2000 行，返回带行号的文本 |
| `write` | 整体覆盖写入文件，自动创建父目录，原子写（含哈希校验） |
| `edit` | 精准替换，要求唯一匹配，原子应用（含哈希校验） |
| `bash` | 执行 shell 命令，输出上限 64KiB，支持进程组终止 |
| `create_task` | 将目标拆解为子任务，各自独立执行并汇总结果 |
| `request_input` | 暂停执行，等待用户输入 |

## 开发

```bash
make run              # 运行 CLI --help
make test             # 运行全部测试
make test-race        # 带竞态检测的测试
make vet              # 静态分析
make bench            # 存储层与应用层基准测试
make test-live        # 需要真实 LLM 的集成测试（需设置环境变量）
make run-tui          # 运行 TUI
```

更深入的设计文档见 [docs/](docs/)：工作流（[workflow.md](docs/workflow.md)）、状态机（[machine.md](docs/machine.md)）、命令协议（[command.md](docs/command.md)）、LLM 层（[llm.md](docs/llm.md)）、模式系统（[mode.md](docs/mode.md)）、TUI（[tui.md](docs/tui.md)）等。
