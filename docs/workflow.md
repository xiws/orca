# Workflow

Agent 驱动的状态机流程引擎。

## 核心思想

**Agent 驱动状态流转，而非程序员预定义路径。**

程序员提供"地图"（有哪些状态、状态之间怎么连通），Agent 自己"开车"（观察当前状态 → 决定做什么 → 决定去哪里）。

```
    程序员的工作：                           Agent 的工作：
    ────────────                           ────────────
    定义状态（Clarify/Plan/Execute/...）     观察当前状态
    定义每个状态的动作                       执行动作
    定义状态间的连通关系                     决定下一步去哪个状态
                                           循环，直到任务完成
```

```
    ┌─────────────────────────────────────────────────────┐
    │              程序员定义的状态地图                      │
    │                                                     │
    │   ┌─────────┐   ┌──────┐   ┌─────────┐            │
    │   │ clarify │──▶│ plan │──▶│ execute │            │
    │   └─────────┘   └──────┘   └────┬────┘            │
    │        │            ▲            │                  │
    │        │            │       ┌────▼────┐            │
    │        └────────────┘       │ verify  │            │
    │                             └────┬────┘            │
    │                                  │                  │
    │        ┌─────────────────────────┘                  │
    │        │                                            │
    │   Agent 在这些状态之间自主流转：                       │
    │   - 需求清晰？跳过 clarify，直接 plan               │
    │   - 计划够了？跳过 plan，直接 execute               │
    │   - 验证失败？回到 plan 或 execute                  │
    │   - 任务完成？进入终态                               │
    └─────────────────────────────────────────────────────┘
```

## Agent — 核心概念

**Agent 是整个系统的核心：它是一个能自主调用工具、自主决定流转的主体。**

Agent 不只是执行预定义流程的工人，它是流程的**驾驶员**。每一轮循环中，Agent 做两件事：

1. **执行**：在当前状态下，调用工具完成任务
2. **决策**：观察执行结果，决定下一步去哪个状态

```
          ┌─────────────────────────────────┐
          │          Agent 循环              │
          │                                 │
          │    ┌──────────┐                 │
          │    │ 观察状态  │                 │
          │    └────┬─────┘                 │
          │         │                       │
          │    ┌────▼─────┐                 │
          │    │ 执行动作  │ ← 调用工具      │
          │    └────┬─────┘                 │
          │         │                       │
          │    ┌────▼─────┐                 │
          │    │ 决策流转  │ ← LLM 决定     │
          │    │ 去哪里？  │   下一状态      │
          │    └────┬─────┘                 │
          │         │                       │
          │    终态？──▶ 退出               │
          │    否  ──▶ 回到"观察状态"       │
          └─────────────────────────────────┘
```

## 核心抽象

### FlowContext — 流程上下文

贯穿整个状态机的生命周期，承载状态间共享的数据。

```go
package workflow

// FlowContext 是状态机执行期间的共享数据容器。
type FlowContext struct {
    Data any            // 业务载荷
    Meta map[string]any // 引擎元信息（状态结果、访问次数等）
}

func NewFlowContext(data any) *FlowContext {
    return &FlowContext{Data: data, Meta: make(map[string]any)}
}
```

### State — 状态

状态机中的一个阶段。每个状态描述"做什么"和"完成后可以去哪里"。

```go
// State 是状态机中的一个阶段。
type State struct {
    // ID 唯一标识。
    ID string

    // Description 状态的描述，Agent 据此理解当前在哪个阶段。
    // 这段文字会注入到 Agent 的提示词中，帮助 Agent 理解上下文。
    Description string

    // Action 状态执行的动作。
    Action Action

    // Transitions 可流转的目标状态列表。
    // Agent 从中选择下一个状态。
    // 空列表表示终态（Agent 不会流转到其他地方）。
    Transitions []string
}
```

**关键设计**：`Transitions` 不是条件边，而是**可达列表**。Agent 看到当前状态能去哪些状态，然后自己决定去哪里。

### Action — 动作

状态要做的事情。

```go
// Action 是状态执行的动作。
type Action interface {
    Execute(ctx *FlowContext) error
    ActionName() string
}
```

### Machine — 状态机

Agent 驱动的状态机引擎。

```go
// Machine 是 Agent 驱动的状态机。
// 程序员定义状态和连通关系，Agent 自主决定流转路径。
type Machine struct {
    States    map[string]*State
    InitialID string
    Runtime   *agent.Runtime
}

// Execute 启动 Agent 驱动的状态机循环。
// Agent 在每轮中：观察当前状态 → 执行动作 → 决定下一状态 → 流转。
func (m *Machine) Execute(ctx *FlowContext) error {
    currentID := m.InitialID

    for {
        state := m.States[currentID]
        if state == nil {
            return fmt.Errorf("state %q not found", currentID)
        }

        // 记录访问
        recordVisit(ctx, currentID)

        // 1. 执行当前状态的动作
        if err := state.Action.Execute(ctx); err != nil {
            return fmt.Errorf("state %s action failed: %w", currentID, err)
        }
        setStateResult(ctx, currentID, ctx.Data)

        // 2. 终态检查：没有可流转的状态 → 结束
        if len(state.Transitions) == 0 {
            return nil
        }

        // 3. Agent 决策：下一状态去哪里
        nextID, err := m.agentDecide(ctx, state)
        if err != nil {
            return fmt.Errorf("agent decision failed at state %s: %w", currentID, err)
        }

        // 4. 流转
        currentID = nextID
    }
}
```

### Agent 决策：agentDecide

这是整个设计的核心——Agent 如何决定下一步去哪里。

```go
// agentDecide 让 Agent 观察当前状态和执行结果，决定下一步去哪个状态。
//
// Agent 看到的信息：
// 1. 当前状态的描述和执行结果
// 2. 可以流转到的目标状态列表（含描述）
// 3. 历史流转路径（已经过哪些状态）
//
// Agent 输出：
// 下一个状态的 ID
func (m *Machine) agentDecide(ctx *FlowContext, current *State) (string, error) {
    // 构建决策提示词
    prompt := m.buildDecisionPrompt(ctx, current)

    // 调用 LLM 决策
    task := agent.NewTask(prompt, "workflow-decision")
    task.SessionInfo.Messages = []llm.ChatMessage{
        {Role: llm.RoleSystem, Content: decisionSystemPrompt},
        {Role: llm.RoleUser, Content: prompt},
    }

    _, result := m.Runtime.RunTask(task)

    // 解析 Agent 的选择
    nextID := parseDecision(result, current.Transitions)
    if nextID == "" {
        return "", fmt.Errorf("agent chose invalid state from %q", current.ID)
    }
    return nextID, nil
}
```

**决策提示词**：

```go
const decisionSystemPrompt = `你是一个流程决策 Agent。
你的任务是观察当前状态的执行结果，决定下一步应该流转到哪个状态。

决策原则：
- 如果当前任务已完成且不需要后续步骤，选择终态
- 如果需求还不清晰，选择 clarify
- 如果需求清晰但没有计划，选择 plan
- 如果有计划且可以执行，选择 execute
- 如果执行完毕需要验证，选择 verify
- 如果验证失败需要修复，选择 execute 或 plan
- 不要重复已经充分完成的状态

你必须从可选状态中选择一个。只输出状态 ID。`

func (m *Machine) buildDecisionPrompt(ctx *FlowContext, current *State) string {
    var sb strings.Builder

    // 当前状态
    fmt.Fprintf(&sb, "当前状态: %s\n", current.ID)
    fmt.Fprintf(&sb, "状态描述: %s\n", current.Description)

    // 执行结果
    if result, ok := GetStateResult(ctx, current.ID); ok {
        fmt.Fprintf(&sb, "\n执行结果:\n%v\n", result)
    }

    // 可选的下一状态
    sb.WriteString("\n可选的下一状态:\n")
    for _, id := range current.Transitions {
        state := m.States[id]
        fmt.Fprintf(&sb, "- %s: %s\n", id, state.Description)
    }

    // 历史路径
    if path, ok := ctx.Meta["path"].([]string); ok {
        sb.WriteString("\n已经过状态: ")
        sb.WriteString(strings.Join(path, " → "))
        sb.WriteString("\n")
    }

    sb.WriteString("\n请选择下一个状态，只输出状态 ID。")
    return sb.String()
}
```

## Action 类型

### ClarifyAction — 澄清需求

```go
type ClarifyAction struct {
    Runtime *agent.Runtime
    Context AgentContextBuilder
    Prompt  string
}

const defaultClarifyPrompt = `你是一个需求分析 Agent。
分析用户的需求，识别歧义和缺失信息。
你可以读取项目文件来理解上下文，但不要修改任何文件。
输出：任务目标、约束条件、不确定的点、建议修改范围。`

func (a *ClarifyAction) Execute(ctx *FlowContext) error {
    goal, extra := a.Context.Build(ctx)
    task := agent.NewTask(goal, "clarify")
    prompt := a.Prompt
    if prompt == "" { prompt = defaultClarifyPrompt }
    task.SessionInfo.Messages = []llm.ChatMessage{
        {Role: llm.RoleSystem, Content: prompt + "\n\n" + extra},
    }
    err, result := a.Runtime.RunTask(task)
    if err != nil { return err }
    setStateResult(ctx, "clarify", result)
    return nil
}

func (a *ClarifyAction) ActionName() string { return "clarify" }
```

### PlanAction — 制定计划

```go
type PlanAction struct {
    Runtime *agent.Runtime
    Context AgentContextBuilder
    Prompt  string
}

const defaultPlanPrompt = `你是一个计划 Agent。
将需求分解为具体的执行步骤。
你可以读取项目文件来了解代码结构，但不要修改任何文件。
输出：步骤列表、依赖关系、风险点。`

func (a *PlanAction) Execute(ctx *FlowContext) error {
    goal, extra := a.Context.Build(ctx)
    task := agent.NewTask(goal, "plan")
    prompt := a.Prompt
    if prompt == "" { prompt = defaultPlanPrompt }
    task.SessionInfo.Messages = []llm.ChatMessage{
        {Role: llm.RoleSystem, Content: prompt + "\n\n" + extra},
    }
    err, result := a.Runtime.RunTask(task)
    if err != nil { return err }
    setStateResult(ctx, "plan", result)
    return nil
}

func (a *PlanAction) ActionName() string { return "plan" }
```

### ExecuteAction — 执行任务

```go
type ExecuteAction struct {
    ID       string
    Name     string
    Runtime  *agent.Runtime
    Prompt   string
    Tools    []string            // nil 表示全部工具
    Context  AgentContextBuilder
    MaxTurns int
}

type AgentContextBuilder interface {
    Build(ctx *FlowContext) (goal string, extraContext string)
}

type AgentContextFunc func(ctx *FlowContext) (goal string, extraContext string)

func (f AgentContextFunc) Build(ctx *FlowContext) (string, string) { return f(ctx) }

func (a *ExecuteAction) Execute(ctx *FlowContext) error {
    goal, extra := a.Context.Build(ctx)
    task := agent.NewTask(goal, a.Name)
    if a.Prompt != "" {
        task.SessionInfo.Messages = []llm.ChatMessage{
            {Role: llm.RoleSystem, Content: a.Prompt + "\n\n" + extra},
        }
    }
    err, result := a.Runtime.RunTask(task)
    if err != nil { return err }
    setStateResult(ctx, a.ID, result)
    return nil
}

func (a *ExecuteAction) ActionName() string { return "execute" }
```

### TransformAction — 数据变换

```go
type TransformAction struct {
    Fn func(ctx *FlowContext) (any, error)
}

func (a *TransformAction) Execute(ctx *FlowContext) error {
    result, err := a.Fn(ctx)
    if err != nil { return err }
    ctx.Data = result
    return nil
}

func (a *TransformAction) ActionName() string { return "transform" }
```

## 流转示意

### Agent 自主决策的各种路径

程序员定义了 5 个状态：clarify → plan → execute → verify → done

Agent 根据情况自主选择路径：

```
路径 A（完整流程）：
    clarify → plan → execute → verify → done

路径 B（需求清晰，跳过 clarify）：
    plan → execute → verify → done

路径 C（简单任务，跳过 clarify 和 plan）：
    execute → verify → done

路径 D（验证失败，回到 plan 重新规划）：
    clarify → plan → execute → verify → plan → execute → verify → done

路径 E（验证失败，直接回到 execute 修复）：
    execute → verify → execute → verify → done

路径 F（执行中发现需要重新理解需求）：
    execute → clarify → plan → execute → done
```

**所有路径都是 Agent 自己决定的**，程序员只定义了状态和连通关系。

### 状态地图 vs Agent 路径

```
    程序员定义的状态地图（连通关系）：

    ┌─────────┐
    │ clarify │◄────────────────────┐
    └────┬────┘                     │
         │                          │
    ┌────▼───┐    ┌─────────┐      │
    │  plan  │◄───│ execute │──────┘
    └────┬───┘    └────┬────┘
         │             │
         │        ┌────▼────┐
         └───────▶│ verify  │
                  └────┬────┘
                       │
                  ┌────▼───┐
                  │  done  │  （终态，无出边）
                  └────────┘

    Agent 实际走的路径（示例 D）：

    ┌─────────┐
    │ clarify │ ●
    └────┬────┘
         │ ●
    ┌────▼───┐
    │  plan  │ ●
    └────    │
         │ ●
    ┌─────────┐
    │ execute │ ●
    └────┬────┘
         │ ●
    ┌────▼────┐
    │ verify  │ ● ← 失败！
    └────┬────┘
         │ ●  ─ ─ ─ ─ ─ ─ ┐
    ┌────▼───┐              │
    │  plan  │ ● ◄ ─ ─ ─ ─ ┘  Agent 决定回到 plan
    └────    │
         │ ●
    ┌─────────┐
    │ execute │ ●
    └────┬────┘
         │ ●
    ┌────▼────┐
    │ verify  │ ● ← 通过
    └────┬────┘
         │ ●
    ┌────▼───┐
    │  done  │ ●  结束
    └────────┘
```

## 数据流转

```go
func setStateResult(ctx *FlowContext, stateID string, result any) {
    results, ok := ctx.Meta["state_results"].(map[string]any)
    if !ok {
        results = make(map[string]any)
        ctx.Meta["state_results"] = results
    }
    results[stateID] = result
}

func GetStateResult(ctx *FlowContext, stateID string) (any, bool) {
    results, ok := ctx.Meta["state_results"].(map[string]any)
    if !ok { return nil, false }
    val, exists := results[stateID]
    return val, exists
}

func recordVisit(ctx *FlowContext, stateID string) {
    path, _ := ctx.Meta["path"].([]string)
    ctx.Meta["path"] = append(path, stateID)
}
```

## 与 create_task 的关系

| 维度 | create_task | workflow 状态机 |
|------|-------------|----------------|
| 谁做编排 | LLM 运行时动态决策 | 程序员定义状态地图，Agent 决定路径 |
| 何时分解 | Agent 执行中按需调用 | 流程启动前状态已定义 |
| 循环 | 不支持 | 天然支持（Agent 可以回到之前的状态） |
| 工具集 | 子任务继承全部工具 | 每个状态可配不同工具集 |

**演进方向**：workflow 逐步替代 `create_task`。

```go
// 之前：LLM 自己调用 create_task 分解任务
// 之后：程序员定义状态地图，Agent 自主流转

machine := &workflow.Machine{
    InitialID: "clarify",
    Runtime:   runtime,
    States: map[string]*workflow.State{
        "clarify": {
            ID:          "clarify",
            Description: "分析需求，识别歧义和缺失信息",
            Action:      &workflow.ClarifyAction{Runtime: runtime, Context: ...},
            Transitions: []string{"plan", "execute"}, // 可以去 plan，也可以跳过直接 execute
        },
        "plan": {
            ID:          "plan",
            Description: "制定执行计划，分解步骤",
            Action:      &workflow.PlanAction{Runtime: runtime, Context: ...},
            Transitions: []string{"execute"},
        },
        "execute": {
            ID:          "execute",
            Description: "调用工具执行任务",
            Action:      &workflow.ExecuteAction{ID: "execute", Runtime: runtime, Tools: ...},
            Transitions: []string{"verify", "done"}, // 可以直接结束，也可以验证
        },
        "verify": {
            ID:          "verify",
            Description: "验证执行结果",
            Action:      &workflow.ExecuteAction{ID: "verify", Runtime: runtime, Tools: []string{"bash"}},
            Transitions: []string{"done", "execute", "plan"}, // 通过→done，失败→execute 或 plan
        },
    },
}
```

## 与 agent.Runtime 的关系

### 问题：两层循环

当前设计存在两层循环：

```
    外层：状态机循环（Machine.Execute）
    ┌──────────────────────────────────────────┐
    │  for each state:                         │
    │    1. 执行 Action                        │
    │       ┌──────────────────────────────┐   │
    │       │ 内层：Agent 循环              │   │
    │       │ (Runtime.execute)            │   │
    │       │  LLM → 工具 → 观察 → 循环    │   │
    │       └──────────────────────────────┘   │
    │    2. Agent 决定下一状态                   │
    │       ┌──────────────────────────────┐   │
    │       │ 又一次 Agent 循环             │   │
    │       │ (仅用于决策，不执行工具)       │   │
    │       └──────────────────────────────┘   │
    │    3. 流转 → 回到 1                      │
    └──────────────────────────────────────────┘
```

每个状态至少 **2 次独立的 Agent 循环**（执行 + 决策），每次都是独立的 Task + Session + LLM 对话。开销大，且决策循环其实不需要完整的 Agent 循环。

### 方案 A：两层分离（当前设计）

状态机循环 ⊃ Agent 循环。每个状态是独立的 Task/Session，决策是独立的 LLM 调用。

```
Machine.Execute:
  for each state:
    Runtime.RunTask(action)    ← 独立 Session，完整 agent 循环
    Runtime.RunTask(decision)  ← 独立 Session，仅决策
```

| 优点 | 缺点 |
|------|------|
| 不改 Runtime | 每个状态 2 次 LLM 调用开销 |
| Session 完全隔离 | 决策上下文和执行上下文断裂 |
| 实现简单 | Agent 看不到执行过程中的工具调用细节 |

### 方案 B：合并为一层

把状态流转变成 Agent 循环**内部**的决策，而不是循环外部的独立调用。

核心思路：**状态流转是一种特殊的工具调用**。Agent 在执行工具的过程中，同时决定下一步去哪里。

```
Runtime.execute（改造后）:
  for turn < MaxTurns:
    LLM Request → response
    if 包含 transition 工具调用:
      提取目标状态 → 记录到 FlowContext
      不执行 transition，而是作为信号返回
    if 包含普通工具调用:
      执行工具
    if 无工具调用:
      return（Agent 完成）
```

具体做法：注册一个 `transition` 工具，Agent 在调用其他工具的同时，可以调用 `transition` 来声明下一步去哪里。

```go
// transition 工具定义
func TransitionTool() Tool {
    return NewTool("transition",
        "声明下一个要流转到的状态。在完成当前状态的工作后调用。",
        ObjectProperty("", map[string]ToolSchema{
            "target": StringProperty("目标状态 ID"),
        }, "target"))
}
```

Agent 的提示词中包含当前状态和可选的下一状态：

```
当前状态: execute
状态描述: 调用工具执行任务
可选的下一状态: verify, done

当你完成当前状态的工作后，调用 transition 工具声明下一步。
```

```
改造后的循环：

    ┌───────────────────────────────────────────┐
    │         只有一个 Agent 循环                │
    │         (Runtime.execute 改造后)           │
    │                                           │
    │  for turn < MaxTurns:                     │
    │    LLM → 工具调用                         │
    │    ├─ read/write/edit/bash → 执行工具     │
    │    └─ transition("verify") → 记录流转     │
    │    无工具调用 → 返回                       │
    │                                           │
    │  状态机检查：是否有 transition？            │
    │  ├─ 有 → 流转到下一状态，启动新 agent 循环 │
    │  └─ 无 → 状态机终止                       │
    └───────────────────────────────────────────┘
```

| 优点 | 缺点 |
|------|------|
| 一次 LLM 调用同时做执行 + 决策 | 需要改 Runtime.execute |
| Agent 能看到全部工具调用细节 | transition 工具的解析逻辑 |
| 决策质量更高（上下文完整） | 每个状态仍需独立 Session |
| 减少 LLM 调用次数 | |

### 方案对比

```
方案 A（两层分离）：

  状态 1:  LLM①(执行) + LLM②(决策)  = 2 次调用
  状态 2:  LLM③(执行) + LLM④(决策)  = 2 次调用
  状态 3:  LLM⑤(执行) + LLM⑥(决策)  = 2 次调用
  总计: 6 次 LLM 调用

方案 B（合并一层）：

  状态 1:  LLM①(执行 + 决策)         = 1 次调用
  状态 2:  LLM②(执行 + 决策)         = 1 次调用
  状态 3:  LLM③(执行 + 决策)         = 1 次调用
  总计: 3 次 LLM 调用
```

**方案 B 的核心优势**：Agent 在调用 read/edit/bash 的同时，已经看到了执行结果，此时让它决定“下一步去哪里”是最自然的——和它决定“下一步调用什么工具”是同一个决策过程。

### 建议：方案 B

方案 B 更贴合 Agent 的工作模式。Agent 本来就在循环中不断决策（下一个工具调什么），把状态流转变成一种特殊的工具调用，只是扩展了决策的范围——从“下一个工具”扩展到“下一个状态”。

对 Runtime 的改动集中在：
1. 新增 `transition` 工具注册
2. `execute` 循环中识别 transition 调用，提取目标状态
3. `RunTask` 返回值增加“目标状态”字段

```go
// 改造后的 Result
type Result struct {
    Content      string
    ToolCalls    []ToolCall
    FinishReason string
    Usage        Usage
    Error        error
    Transition   string // 新增：Agent 选择的下一状态（空字符串表示无流转）
}

// 改造后的 execute
func (r *Runtime) execute(task *Task) (error, string) {
    // ... 现有循环逻辑 ...
    for turn := 0; turn < MaxTurns; turn++ {
        res := requester.Request(...)
        // ... 现有逻辑 ...

        calls := llm.EnsureToolCallIDs(res.ToolCalls)

        // 检查是否包含 transition 调用
        for _, call := range calls {
            if call.Name == "transition" {
                task.Transition = parseTransitionTarget(call.Arguments)
            }
        }

        // 过滤掉 transition 调用，只执行普通工具
        normalCalls := filterNonTransition(calls)
        if len(normalCalls) == 0 && task.Transition != "" {
            return nil, res.Content // 有流转目标，结束当前状态
        }
        if len(normalCalls) == 0 {
            return nil, res.Content
        }
        r.executeCommand(task, normalCalls)
    }
    return ErrMaxTurns, ""
}
```

状态机引擎简化为：

```go
func (m *Machine) Execute(ctx *FlowContext) error {
    currentID := m.InitialID

    for {
        state := m.States[currentID]
        recordVisit(ctx, currentID)

        // 执行状态动作（内部 Agent 循环同时完成执行 + 决策）
        transition, err := state.Action.Execute(ctx)
        if err != nil {
            return err
        }
        setStateResult(ctx, currentID, ctx.Data)

        // 无流转目标 → 终态
        if transition == "" {
            return nil
        }

        // 验证流转目标合法
        if _, ok := m.States[transition]; !ok {
            return fmt.Errorf("invalid transition %q from state %q", transition, currentID)
        }
        currentID = transition
    }
}
```

## 与现有架构的集成

```
cmd/cli ──▶ Machine.Execute(ctx)
                │
                ▼
        ┌────────────────────────────────┐
        │     Agent 驱动的状态机引擎      │
        │                                │
        │  循环：                         │
        │  1. 执行状态动作                │
        │     (Agent 循环同时完成          │
        │      执行 + 决策)               │
        │  2. 检查 transition            │
        │  3. 流转 → 回到 1              │
        └──────────────┬─────────────────┘
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
     ClarifyAction  PlanAction  ExecuteAction
          │            │            │
          ▼            ▼            ▼
     ┌──────────────────────────────────┐
     │     agent.Runtime（改造后）       │
     │                                  │
     │  Agent 循环内部：                 │
     │  LLM → 工具调用                  │
     │  ├─ read/write/edit/bash → 执行  │
     │  └─ transition → 记录流转目标    │
     └──────────────┬───────────────────┘
                    │
            ┌───────┴───────┐
            ▼               ▼
         LLM 对话        工具调用
         (思考决策)    (read/write/edit/bash)
                       + transition
```

**集成原则**：
1. **Agent 驱动**：状态流转由 Agent（LLM）在工具调用中同时决策，不是独立的 LLM 调用。
2. **程序员提供地图**：定义状态、动作、连通关系，但不决定路径。
3. **Session 隔离**：每个状态独立的 Session，对话历史不跨状态。
4. **Event 透传**：Action 内部的 Runtime 照常发布事件。
5. **工具集控制职责**：Clarify/Plan 只给 {read}，Execute 给完整工具集。
6. **transition 是特殊工具**：Agent 在执行工具的同时决定下一步去哪里。

## 包结构

```
pkg/workflow/
├── context.go    # FlowContext
├── state.go      # State + Action 接口
├── machine.go    # Machine 引擎（Agent 驱动循环 + agentDecide）
├── clarify.go    # ClarifyAction
├── plan.go       # PlanAction
├── execute.go    # ExecuteAction + AgentContextBuilder
└── transform.go  # TransformAction
```

## 使用示例

### 完整 Agent 驱动流程

```go
runtime := agent.NewRuntime()

machine := &workflow.Machine{
    InitialID: "clarify",
    Runtime:   runtime,
    States: map[string]*workflow.State{
        "clarify": {
            ID:          "clarify",
            Description: "分析用户需求，识别歧义和缺失信息。只读项目文件，不做修改。",
            Action: &workflow.ClarifyAction{
                Runtime: runtime,
                Context: workflow.AgentContextFunc(func(ctx *workflow.FlowContext) (string, string) {
                    return "分析用户需求", ctx.Data.(string)
                }),
            },
            Transitions: []string{"plan", "execute"},
        },
        "plan": {
            ID:          "plan",
            Description: "制定执行计划，将需求分解为具体步骤。只读项目文件，不做修改。",
            Action: &workflow.PlanAction{
                Runtime: runtime,
                Context: workflow.AgentContextFunc(func(ctx *workflow.FlowContext) (string, string) {
                    clarify, _ := workflow.GetStateResult(ctx, "clarify")
                    return "制定执行计划", fmt.Sprintf("需求分析:\n%v", clarify)
                }),
            },
            Transitions: []string{"execute"},
        },
        "execute": {
            ID:          "execute",
            Description: "调用工具执行任务。可以读取、编写、编辑文件和运行命令。",
            Action: &workflow.ExecuteAction{
                ID: "execute", Name: "执行任务", Runtime: runtime,
                Tools: []string{"read", "write", "edit", "bash"},
                Context: workflow.AgentContextFunc(func(ctx *workflow.FlowContext) (string, string) {
                    plan, hasPlan := workflow.GetStateResult(ctx, "plan")
                    if hasPlan {
                        return "按计划执行", fmt.Sprintf("执行计划:\n%v", plan)
                    }
                    clarify, _ := workflow.GetStateResult(ctx, "clarify")
                    return "执行任务", fmt.Sprintf("需求:\n%v", clarify)
                }),
            },
            Transitions: []string{"verify", "done"},
        },
        "verify": {
            ID:          "verify",
            Description: "运行测试验证执行结果，报告通过或失败。",
            Action: &workflow.ExecuteAction{
                ID: "verify", Name: "验证", Runtime: runtime,
                Tools: []string{"read", "bash"},
                Context: workflow.AgentContextFunc(func(ctx *workflow.FlowContext) (string, string) {
                    return "验证结果", "运行 go test ./... 并报告结果"
                }),
            },
            Transitions: []string{"done", "execute", "plan"},
        },
    },
}

ctx := workflow.NewFlowContext("重构 auth 模块，将 session 管理改为文件持久化")
if err := machine.Execute(ctx); err != nil {
    panic(err)
}
```

**Agent 可能走的路径**：
- 需求清晰 → 跳过 clarify → `plan → execute → verify → done`
- 验证失败 → 直接修复 → `plan → execute → verify → execute → verify → done`
- 简单任务 → `execute → done`
- 验证发现根本问题 → `execute → verify → clarify → plan → execute → verify → done`

### 简单场景：只有 Execute

```go
machine := &workflow.Machine{
    InitialID: "fix",
    Runtime:   runtime,
    States: map[string]*workflow.State{
        "fix": {
            ID:          "fix",
            Description: "修复 bug，调用工具定位问题并修复。",
            Action: &workflow.ExecuteAction{
                ID: "fix", Name: "修复", Runtime: runtime,
                Tools: []string{"read", "edit", "bash"},
                Context: workflow.AgentContextFunc(func(ctx *workflow.FlowContext) (string, string) {
                    return "修复 issue #42", "token 过期未刷新"
                }),
            },
            // 无 Transitions → 终态，执行完毕后状态机结束
        },
    },
}

ctx := workflow.NewFlowContext(nil)
machine.Execute(ctx)
```
