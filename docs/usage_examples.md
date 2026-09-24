# 使用示例

本文档通过具体场景展示 Orca 的各项功能。

## 目录

- [基础用法](#基础用法)
- [工作流模式](#工作流模式)
- [Provider 与模型配置](#provider-与模型配置)
- [文件上下文](#文件上下文)
- [会话管理](#会话管理)
- [运行管理](#运行管理)
- [配置文件](#配置文件)
- [TUI 交互界面](#tui-交互界面)

---

## 基础用法

### 执行一个任务

最简单的用法——给出目标，Orca 自主调用工具完成：

```bash
orca "为 internal/llm/openai.go 生成单元测试文件"
```

模型会读取源文件、编写测试、运行验证，全程在终端输出执行过程。

### 指定工作目录

Orca 默认以当前目录为工作区，所有工具操作限制在该目录内：

```bash
cd ~/projects/my-app
orca "分析项目结构并给出优化建议"
```

---

## 工作流模式

通过 `-mode` 选择不同的工作流模式，按任务性质匹配最合适的执行策略。

### Ask 模式：只读问答

只读取代码、不修改任何文件，适合理解代码和定位问题：

```bash
orca -mode ask "internal/agent 包的调用链是怎样的"
```

### Plan 模式：制定计划

分析需求并输出执行计划，但暂不动手修改代码。适合大型重构前先做方案：

```bash
orca -mode plan "将 internal/store 从 JSON 文件迁移到 SQLite"
```

### Code 模式：日常开发

默认模式，可以读写文件、执行命令，适合日常开发任务：

```bash
orca -mode code "重构 internal/agent 包的错误处理，统一使用 fmt.Errorf 包装"
```

### Agent 模式：自主执行

完整闭环——澄清需求、制定计划、执行、验证、修复，适合复杂需求：

```bash
orca -mode agent "实现 CLI 的 session 持久化，使用 SQLite 存储"
```

### Review 模式：代码评审

只读评审代码质量、安全性、性能，不修改文件：

```bash
orca -mode review "评审 internal/tools 包的安全策略实现"
```

### Test 模式：编写与运行测试

创建测试、运行测试、分析失败并自动修复：

```bash
orca -mode test "为 pkg/workflow 状态机编写完整的单元测试"
```

### Terminal 模式：Shell 操作

专注构建、运行、安装依赖等 shell 操作：

```bash
orca -mode terminal "安装项目依赖并运行全部测试"
```

### Deliberate 模式：多角度辩论

多个 AI 角色从不同视角分析问题，适合设计和决策：

```bash
orca -mode deliberate "项目应该用 gRPC 还是 REST 做内部通信"
```

---

## Provider 与模型配置

### 使用 Ollama 本地模型

```bash
orca -p ollama -m "ornith:latest" "解释 pkg/command 的设计"
```

### 使用 Otter 平台接入 ChatGPT

```bash
orca -p otter -m chatgpt "为这个项目编写 README"
```

### 使用 Otter 平台接入 DeepSeek

```bash
orca -p otter -m deepseek "分析 internal/workflow 的状态流转逻辑"
```

> `-p` 和 `-m` 必须成对使用。不指定时取 `setting.json` 中的默认值。

---

## 文件上下文

### 附加单个文件

用 `-f` 把文件内容作为上下文附加到请求中：

```bash
orca -f main.go "解释这个入口文件的启动流程"
```

### 附加多个文件

`-f` 可重复使用，适合需要跨文件分析的场景：

```bash
orca -f internal/agent/core/runtime.go -f internal/agent/workflow/runner.go \
  "分析 Runtime 和 WorkflowRunner 的协作方式"
```

### 结合模式使用

文件附加与模式可以组合：

```bash
orca -mode review -f internal/tools/gateway.go "评审这个工具网关的并发安全性"
```

---

## 会话管理

Orca 支持持久化会话，可在同一个会话中连续提交多个任务。

### 查看会话列表

```bash
orca session list
```

输出示例：

```
ID                   TITLE                           CREATED
1234567890123456789  为 openai.go 生成单元测试         2026-09-20 14:30
9876543210987654321  重构 agent 包错误处理             2026-09-21 10:15
```

### 在已有会话中继续

使用 `-session` 在同一会话中提交新任务，模型可以看到之前的对话历史：

```bash
orca -session 1234567890123456789 "接下来优化测试的覆盖率"
```

### 查看会话详情

```bash
orca session show 1234567890123456789
```

### 删除会话

```bash
# 删除指定会话
orca session rm 1234567890123456789

# 删除所有会话
orca session rm --all
```

---

## 运行管理

### 终端使用模式

**`orca "任务"` 会阻塞当前终端**，实时显示执行过程。实际执行由进程内的后台 worker 完成，但 CLI 会一直等待直到任务结束。

这意味着：

- **单终端场景**：任务正常完成时没问题。但如果任务进入 `waiting` 状态（需要输入或审批），你无法在同一个终端响应。
- **双终端场景**：当任务需要输入/审批时，打开第二个终端窗口执行 `orca run respond/approve` 命令。

```bash
# 终端 1：提交任务（阻塞，显示实时输出）
$ orca -mode agent "实现新功能"
session=123 task=456 run=789 state=running
[实时显示模型输出和工具调用...]
# 模型调用 request_input，进入 waiting 状态
# 终端仍然阻塞，等待响应...

# 终端 2：回复输入请求
$ orca run respond 987 "用户提供的信息"

# 终端 1：继续显示执行过程，直到完成
```

### 什么时候需要 `orca run`？

每次执行 `orca "任务"` 都会自动创建一个 **Run**（运行实例）。大多数情况下，任务会直接执行完毕，你不需要额外操作。

`orca run` 子命令用于**任务已经在运行或中断后**的管理场景：

```
你执行 orca "生成单元测试"
        │
        ▼
  自动创建 Run（状态：queued → running）
        │
        ├─ 正常完成 → succeeded（结束，不需要 run 子命令）
        ├─ 执行失败 → failed
        ├─ 进程崩溃/Ctrl+C → interrupted  → 用 orca run resume 恢复
        ├─ 模型请求输入 → waiting          → 用 orca run respond 回复
        ├─ 模型请求审批 → waiting          → 用 orca run approve 审批
        └─ 崩溃时有未知副作用 → reconciling → 用 orca run reconcile 确认
```

**典型场景**：

- 任务正在执行，你想查看进度 → `orca run show`
- 任务执行到一半你关了终端，想继续 → `orca run resume`
- 模型调用 `request_input` 工具暂停了，需要回复 → `orca run respond`
- 模型要写文件或执行命令，需要你审批 → `orca run approve`
- 任务崩溃且有未知副作用（如 bash 命令可能已执行），需要手动确认 → `orca run reconcile`

### Run 的状态

| 状态 | 含义 | 后续操作 |
| --- | --- | --- |
| `queued` | 已创建，等待执行 | 自动进入 running |
| `running` | 正在执行 | 等待完成或中断 |
| `waiting` | 等待用户输入或审批 | `respond` / `approve` / `reject` |
| `interrupted` | 中断（崩溃、Ctrl+C） | `resume` 恢复 |
| `reconciling` | 有未知副作用需确认 | `reconcile` 手动确认 |
| `succeeded` | 成功完成（终态） | 无 |
| `failed` | 失败（终态） | `retry` 重试 |
| `cancelled` | 已取消（终态） | 无 |

### 查看运行列表

```bash
orca run list
```

### 重放运行事件

查看某次运行的完整执行过程，包括工具调用和模型输出：

```bash
orca run show 12345
```

从指定序号开始重放（适合增量查看）：

```bash
orca run show 12345 --after 50
```

### 恢复中断的运行

当运行因崩溃或手动中断而停止时，可以恢复执行：

```bash
orca run resume 12345
```

### 重试任务

对失败的任务创建新的运行：

```bash
orca run retry TASK_ID
```

### 回复等待中的输入

模型执行过程中可能通过 `request_input` 工具请求用户输入，此时运行会安全暂停：

```bash
# 回复输入请求
orca run respond INPUT_ID "用户提供的回答"
```

### 审批

模型执行高风险操作时可能请求审批：

```bash
# 审批通过
orca run approve INPUT_ID

# 审批拒绝
orca run reject INPUT_ID
```

### 取消运行

```bash
orca run cancel 12345
```

### 手动确认异常结果

当运行结果状态为 unknown 时，可以手动标记为失败，防止重放：

```bash
orca run reconcile 12345 --note "已确认结果无效"
```

---

## 配置文件

Orca 的配置文件分布在两个位置，项目目录优先于用户主目录：

| 位置 | 作用域 |
| --- | --- |
| `./.orca/` | 当前项目（优先） |
| `~/.orca/` | 全局默认 |

### 配置 Provider：models.json

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
          "id": "qwen2.5:14b",
          "name": "Qwen 2.5 14B",
          "contextWindow": 65536,
          "supportsTools": true
        }
      ]
    },
    "dashscope": {
      "name": "DashScope",
      "api": "openai-completions",
      "baseUrl": "https://dashscope.aliyuncs.com/compatible-mode/v1",
      "apiKey": "${DASHSCOPE_API_KEY}",
      "models": [
        {
          "id": "qwen-plus",
          "name": "Qwen Plus",
          "contextWindow": 131072,
          "supportsTools": true
        }
      ]
    },
    "otter": {
      "name": "Otter (Web)",
      "api": "otter",
      "models": [
        { "id": "chatgpt", "name": "ChatGPT (otter)", "contextWindow": 32768, "supportsTools": false },
        { "id": "deepseek", "name": "DeepSeek (otter)", "contextWindow": 65536, "supportsTools": false }
      ]
    }
  }
}
```

- `api` 为 `openai-completions` 时走 OpenAI 兼容接口，支持原生 function calling
- `api` 为 `otter` 时走 otter Web 平台，工具调用走文本 JSON 协议
- `apiKey` 支持 `${ENV_VAR}` 语法引用环境变量
- `supportsTools: false` 的模型会通过系统提示词下发工具说明

### 设置默认 Provider：setting.json

```json
{
  "defaultProvider": "ollama",
  "defaultModel": "qwen2.5:14b"
}
```

设置后直接运行 `orca "任务"` 即可，无需每次指定 `-p` 和 `-m`。

### 自定义系统提示词：system_prompt.md

将自定义提示词写入 `.orca/system_prompt.md`，Orca 会在每次请求中使用该提示词替代默认值。

### 覆盖系统提示词

用 `-s` 在命令行直接指定提示词文件：

```bash
orca -s my_prompt.md "按照自定义规则分析代码"
```

---

## TUI 交互界面

Orca 提供基于 Bubble Tea 的终端交互界面，支持持续对话：

```bash
./orca-tui
```

TUI 界面中可以直接输入任务、查看执行过程、管理会话，无需反复输入命令行参数。

---

## 典型工作流示例

### 场景一：为新模块编写测试

```bash
# 1. 先用 ask 模式理解代码
orca -mode ask "解释 internal/store 包的接口设计"

# 2. 用 test 模式编写并运行测试
orca -mode test "为 internal/store 包的所有接口编写单元测试"

# 3. 如果测试失败，在同一会话中继续修复
orca -session <session-id> "分析失败原因并修复"
```

### 场景二：大型重构

```bash
# 1. 先用 plan 模式制定方案
orca -mode plan "将 pkg/event 从依赖 internal/base 改为独立包"

# 2. 确认方案后，用 agent 模式执行
orca -mode agent -session <session-id> "按照计划执行重构"

# 3. 用 review 模式检查重构结果
orca -mode review "检查重构后的代码质量和潜在问题"
```

### 场景三：使用不同 Provider 对比

```bash
# 用本地 Ollama 快速分析
orca -p ollama -m "qwen2.5:14b" "分析这个函数的时间复杂度"

# 用 ChatGPT 做更深入的分析
orca -p otter -m chatgpt "分析这个函数的时间复杂度并给出优化方案"
```
