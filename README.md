# Orca

Orca 是一个用 Go 编写的 AI Agent 命令行工具。你给一个目标，模型自主调用本地工具（读写文件、执行命令等）逐步完成。需要你回答或审批时，它在**同一个进程内**直接问你要答案。

```bash
$ orca "为 internal/model 生成单元测试"
```

## 核心概念

| 概念 | 说明 |
| --- | --- |
| **Session** | 一次交互的时间线（单个进程内） |
| **Task** | 版本化的业务目标 |
| **Run** | 任务版本的一次执行，包含工作流节点、策略与预算 |
| **Invocation** | 单次角色执行，拥有私有消息线程 |

一次 `orca "..."` 从提交跑到结束。状态只活在进程内存里，**进程退出即结束**：不跨进程、不跨崩溃恢复，
也所以每次调用都是一个独立的新会话。

## 特性

- **多 Provider**：兼容 OpenAI Chat Completions 协议的任意服务（Ollama、DashScope 等），以及 otter Web 平台
- **工作流模式**：ask / code / plan / agent / review / test / terminal / deliberate
- **内置工具**：read / write / edit / bash / create_task / request_input
- **双重工具协议**：支持原生 function calling 的 Provider 直接下发工具声明；不支持的走文本命令协议
- **交互式审批**：写文件、执行命令前在终端里直接问你，绑定的是那一份精确参数
- **流式输出**：模型输出实时打印
- **历史可读**：事件以 JSONL 追加到 `.orca/history.jsonl`，一行一个 JSON，`cat` / `jq` 直接看

## 构建

要求 Go 1.26+。

```bash
make build          # 构建 ./orca
make publish        # 构建并安装到 $GOPATH/bin
```

或直接：

```bash
go build -o orca ./cmd/cli
```

## 快速开始

```bash
# 执行任务
orca "为 internal/model 生成单元测试文件"

# 指定工作流模式
orca -mode code "重构 internal/agent 包的错误处理"

# 指定 Provider 与模型（须成对使用）
orca -p otter -m chatgpt "这个项目做了什么"

# 附加文件作为上下文（可重复）
orca -f main.go -f README.md "解释这两个文件"
```

执行过程中如果需要你：

```text
--- approval required ---
{
  "kind": "approval",
  "workspace": "/path/to/repo",
  "tool": "bash",
  "call_id": "call_1",
  "arguments": {"command": "go test ./...", "workdir": "."},
  "notice": "Approve only these exact arguments in this workspace. Bash is not a sandbox and can access outside this workspace."
}
approve? [y/N]
```

回答 `y` 继续，其它任何输入都视为拒绝。`request_input` 类型则提示 `answer:`，读取一行文本。

也可以把回答通过管道喂进去：

```bash
printf 'y\n' | orca "跑一遍测试并修掉失败"
```

## 命令行用法

```text
Usage:
  orca [options] PROMPT
```

### 选项

| 选项 | 说明 |
| --- | --- |
| `-p, --provider <name>` | 指定模型 Provider，须与 `-m` 一起使用；取值为 `models.json` 中的 provider key，如 `otter`、`ollama` |
| `-m, --model <id>` | 指定模型 ID，须与 `-p` 一起使用，如 `chatgpt`、`deepseek` |
| `-mode <name>` | 工作流模式：ask / code / plan / agent / review / test / terminal / deliberate |
| `-s, --sp <file>` | 用文件内容覆盖系统提示词 |
| `-f, --file <path>` | 附加文件到本次请求，可重复使用；文件以 `<file path="...">` 包裹后放入用户消息 |
| `-h, --help` | 显示完整帮助 |

> `-p` / `-m` 缺省时使用 `setting.json` 中的默认 Provider 与模型。

## 历史记录

每次执行的事件都会追加到 `./.orca/history.jsonl`，一行一个对象：

```json
{"time":1761600000,"seq":12,"run":123,"invocation":456,"kind":"tool","content":"bash go test ./..."}
```

直接读就行：

```bash
cat .orca/history.jsonl
jq 'select(.kind=="failed")' .orca/history.jsonl
```

历史是只写的旁路：它不参与执行，也不用于恢复。进程退出后状态即消失，想继续就重新提交。

## 配置文件

配置来自两个层级，项目目录优先于用户主目录（同名文件做字段级合并，项目级生效）：

| 文件 | 位置 | 说明 |
| --- | --- | --- |
| `models.json` | `./.orca/models.json`、`~/.orca/models.json` | Provider 与模型定义 |
| `setting.json` | `./.orca/setting.json`、`~/.orca/setting.json` | 默认 Provider / 模型 |
| `system_prompt.md` | `./.orca/system_prompt.md`、`~/.orca/system_prompt.md` | 系统提示词模板 |
| `otter_prompt.md` | `./.orca/otter_prompt.md`、`~/.orca/otter_prompt.md` | otter 专用提示词 |
| `history.jsonl` | `./.orca/history.jsonl` | 追加写入的事件历史 |

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
cmd/cli          CLI：参数解析、事件展示、待输入时读标准输入
internal/
  bootstrap      依赖装配：config → store → tools → providers → agent → workflow → app
  config         配置加载与合并，环境变量展开
  domain         领域类型：Session / Task / Run / Invocation / Event / Mutation
  store          内存状态 + history.jsonl 追加日志
  model          模型协议：Client 接口、Message / Call / Tool / Cursor
  providers      Provider 实现：OpenAI 兼容、otter
  tools          工具网关：策略、审批、幂等重放、工作区隔离
  agent          执行器：Invocation 状态机
  workflow       工作流编排：节点推进、委派、验证、有限修复
  app            服务层：调度、发布订阅、后台 Worker
```

### 存储层

状态全部在内存里，用一把互斥锁保护。`Store.Apply(Mutation)` 先校验整组变更再一次性写入，
版本号仍是乐观锁（并发调用之间能检出冲突），但**没有事务、没有数据库、没有崩溃恢复**。
唯一的持久化是追加写 `history.jsonl`，写失败不影响执行。

### 执行模型

Run 形成树结构（`RootRunID` / `ParentRunID` / `Depth`），WorkflowRunner 编排整棵树，
AgentRunner 处理单次 Invocation。ToolGateway 是工具执行的唯一入口，负责策略检查、
审批与幂等重放。

外部副作用的结果如果是未知的（进程在 bash 执行期间被杀），这一轮直接失败，不做重放。
工作区有没有被改动，看 `git status`。

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
```

`docs/` 下的多数文档描述的是已经删除的实现，读之前先看 [docs/README.md](docs/README.md) 的状态索引；
当前实现以 [quick.md](quick.md) 为准。
