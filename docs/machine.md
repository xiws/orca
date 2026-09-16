# 可编程有向状态流转引擎

包路径：`pkg/workflow`

## 1. 概述

提供一个通用状态流转引擎。

目标：

- 支持任意状态跳转
- 支持状态回退
- 支持分支流程
- 支持跳过流程
- 支持权限/条件控制
- 支持生命周期事件

状态机模型：

```
State = 节点
Transition = 边
Workflow = 有向图
```

例如：

```
                 reject
             +----------+
             |          |
             v          |
APPLY ----------------> REJECTED
  |
  | approve
  v
PASSED
  |
  | finish
  v
FINISHED

特殊:
APPLY ----special_pass----> FINISHED
```

---

## 2. 包结构

```
pkg/workflow/
├── context.go      # Context 类型
├── errors.go       # 错误定义
├── guard.go        # Guard 接口
├── condition.go    # Condition 接口
├── hook.go         # Hook 接口
├── handler.go      # Handler 接口
├── state.go        # State + StateBuilder
├── transition.go   # Transition + TransitionBuilder
├── workflow.go     # Workflow（注册状态和流转）
├── engine.go       # Engine + Execute + Run
└── workflow_test.go
```

---

## 3. 核心对象

### Context

执行上下文，用于在 Guard/Condition/Hook 间传递数据。

```go
type Context map[string]any
```

### State

表示一个状态节点。

```go
type State struct {
    // Name 状态唯一标识。
    Name string

    // Terminal 是否为终态。
    Terminal bool

    // Handler 节点业务处理逻辑。
    // Engine.Run 进入该状态时调用；终态节点可为 nil。
    Handler Handler
}
```

示例：

```go
wf.State("APPLY")
wf.State("FINISHED").Terminal()
```

### Transition

表示状态之间的一条流转路径。

```go
type Transition struct {
    From       string      // 起始状态
    To         string      // 目标状态
    Action     string      // 用户执行动作
    Conditions []Condition // 条件列表
    Guards     []Guard     // 权限控制列表
    Hooks      []Hook      // 生命周期事件列表
}
```

示例：

```go
wf.From("APPLY").To("REJECTED").Action("reject")
```

生成：

```
APPLY
  |
  | reject
  v
REJECTED
```

---

## 4. 接口定义

### Guard — 权限控制

```go
type Guard interface {
    Check(ctx Context) error
}
```

示例：

```go
type AdminGuard struct{}

func (AdminGuard) Check(ctx Context) error {
    role, _ := ctx["Role"].(string)
    if role != "admin" {
        return errors.New("admin only")
    }
    return nil
}
```

使用：

```go
wf.From("APPLY").To("FINISHED").
    Action("special_pass").
    Guard(AdminGuard{})
```

### Condition — 业务条件

```go
type Condition interface {
    Evaluate(ctx Context) bool
}
```

示例：金额低于 100 万自动通过。

```go
wf.From("APPLY").To("FINISHED").
    Action("auto_finish").
    Condition(AmountLessThan(1000000))
```

### Hook — 生命周期事件

```go
type Hook interface {
    Before(ctx Context) error
    After(ctx Context) error
}
```

执行顺序：

```
Guard → Condition → Before Hook → 状态流转 → After Hook
```

### Handler — 节点业务逻辑

```go
type Handler interface {
    Handle(ctx Context) (action string, err error)
}
```

Handler 是状态节点的业务处理逻辑。`Engine.Run` 进入非终态时调用 Handler.Handle，返回的 action 决定走哪条 transition。

示例：

```go
type ExecuteHandler struct{}

func (ExecuteHandler) Handle(ctx workflow.Context) (string, error) {
    // 执行业务逻辑...
    return "complete", nil // 返回 "complete" 触发 execute→verify 流转
}
```

使用：

```go
wf.State("execute").Handler(ExecuteHandler{})
wf.State("verify").Handler(VerifyHandler{})
wf.State("done").Terminal() // 终态不需要 Handler
```

---

## 5. 错误定义

```go
var (
    ErrInvalidTransition  = errors.New("invalid transition: no matching action")
    ErrForbidden          = errors.New("forbidden: guard check failed")
    ErrConditionNotMet    = errors.New("condition not met")
    ErrStateNotFound      = errors.New("state not found")
    ErrStateAlreadyExists = errors.New("state already exists")
)
```

| 错误 | 触发场景 |
|------|---------|
| `ErrStateNotFound` | `CurrentState` 不在已注册状态中 |
| `ErrInvalidTransition` | 当前状态无匹配 `Action` 的流转 |
| `ErrForbidden` | Guard.Check 返回 error |
| `ErrConditionNotMet` | Condition.Evaluate 返回 false |

---

## 6. Workflow 定义接口

### 创建流程

```go
func NewWorkflow(name string) *Workflow
```

### 注册状态

```go
func (w *Workflow) State(name string) *StateBuilder
```

### 注册流转

```go
func (w *Workflow) From(state string) *TransitionBuilder
```

TransitionBuilder 链式方法：

```go
func (b *TransitionBuilder) To(state string) *TransitionBuilder
func (b *TransitionBuilder) Action(action string) *TransitionBuilder
func (b *TransitionBuilder) Guard(guard Guard) *TransitionBuilder
func (b *TransitionBuilder) Condition(cond Condition) *TransitionBuilder
func (b *TransitionBuilder) Hook(hook Hook) *TransitionBuilder
```

注意：`Action()` 调用时将 Transition 注册到 Workflow。`Guard`/`Condition`/`Hook` 可在 `Action()` 前后调用。

---

## 7. 完整流程定义示例

业务：申请审批。

```
                         resubmit
                    +-------------+
                    |             |
                    v             |
APPLY ----reject--> REJECTED
  |
  | approve
  v
PASSED
  |
  | finish
  v
FINISHED

APPLY ----special_pass----> FINISHED
```

代码：

```go
wf := NewWorkflow("approval")

// 注册状态
wf.State("APPLY")
wf.State("REJECTED")
wf.State("PASSED")
wf.State("FINISHED").Terminal()

// 注册流转
wf.From("APPLY").To("PASSED").Action("approve")        // 通过
wf.From("APPLY").To("REJECTED").Action("reject")       // 驳回
wf.From("REJECTED").To("APPLY").Action("resubmit")     // 驳回重新申请
wf.From("PASSED").To("FINISHED").Action("finish")      // 完成
wf.From("APPLY").To("FINISHED").Action("special_pass"). // 特批跳过
    Guard(AdminGuard{})
```

---

## 8. Engine 执行接口

### 创建执行引擎

```go
func NewEngine(wf *Workflow) *Engine
```

### 输入

```go
type ExecuteRequest struct {
    InstanceID   string  // 当前实例 ID
    CurrentState string  // 当前状态
    Action       string  // 执行动作
    Context      Context // 执行上下文
}
```

### 输出

```go
type ExecuteResult struct {
    From         string // 原状态
    To           string // 新状态
    Action       string // 执行动作
    TransitionID string // 流转标识（InstanceID 非空时生成）
}
```

### 执行流程

```
ExecuteRequest
      |
      v
  查询当前状态 ──不存在──> ErrStateNotFound
      |
      v
  查找匹配 Transition（From + Action）
      |
      v
  匹配? ──NO──> ErrInvalidTransition
      |
     YES
      |
      v
  执行 Guard ──失败──> ErrForbidden
      |
      v
  执行 Condition ──失败──> ErrConditionNotMet
      |
      v
  Before Hook ──失败──> error（流转不发生）
      |
      v
  状态流转（记录 From/To）
      |
      v
  After Hook
      |
      v
  返回 ExecuteResult
```

---

## 9. Transition 查找规则

输入：`CurrentState: APPLY`，`Action: approve`

查找：

```
Transitions:
APPLY
 +-- reject
 +-- approve      ← 匹配
 +-- special_pass

返回: APPLY -> PASSED
```

如果 `APPLY + unknown_action`，返回 `ErrInvalidTransition`。

---

## 10. 状态持久化职责

状态机不保存业务状态，业务负责持久化：

```go
result, err := engine.Execute(req)
if err == nil {
    order.Status = result.To
    repository.Save(order)
}
```

---

## 11. 并发控制

推荐业务层乐观锁：

```sql
-- 表结构
ALTER TABLE orders ADD COLUMN version INT DEFAULT 0;

-- 更新时带版本校验
UPDATE orders
SET status = 'PASSED', version = version + 1
WHERE id = 10001 AND status = 'APPLY' AND version = 1;
```

---

## 12. 使用指南

### 快速开始

三步走：定义流程 → 创建引擎 → 执行流转。

#### 第一步：定义流程

```go
import "github.com/xiws/orca/pkg/workflow"

wf := workflow.NewWorkflow("approval")

// 注册状态
wf.State("APPLY")
wf.State("PASSED")
wf.State("REJECTED")
wf.State("FINISHED").Terminal()

// 注册流转
wf.From("APPLY").To("PASSED").Action("approve")
wf.From("APPLY").To("REJECTED").Action("reject")
wf.From("REJECTED").To("APPLY").Action("retry")
wf.From("PASSED").To("FINISHED").Action("finish")
```

#### 第二步：创建引擎

```go
engine := workflow.NewEngine(wf)
```

#### 第三步：执行流转

```go
result, err := engine.Execute(workflow.ExecuteRequest{
    InstanceID:   "10001",
    CurrentState: "APPLY",
    Action:       "approve",
})
if err != nil {
    log.Fatal(err)
}
fmt.Printf("%s -> %s\n", result.From, result.To)
// 输出: APPLY -> PASSED
```

### 使用 Guard（权限控制）

```go
// 定义 Guard
type AdminGuard struct{}

func (AdminGuard) Check(ctx workflow.Context) error {
    role, _ := ctx["Role"].(string)
    if role != "admin" {
        return errors.New("admin only")
    }
    return nil
}

// 注册流转时附加 Guard
wf.From("APPLY").To("FINISHED").
    Action("special_pass").
    Guard(AdminGuard{})

// 执行时传入上下文
result, err := engine.Execute(workflow.ExecuteRequest{
    InstanceID:   "10001",
    CurrentState: "APPLY",
    Action:       "special_pass",
    Context:      workflow.Context{"Role": "admin"},
})
// Role != "admin" 时返回 ErrForbidden
```

### 使用 Condition（业务条件）

```go
// 定义 Condition
type AmountLessThan struct {
    Limit int
}

func (c AmountLessThan) Evaluate(ctx workflow.Context) bool {
    amount, _ := ctx["Amount"].(int)
    return amount < c.Limit
}

// 注册流转时附加 Condition
wf.From("APPLY").To("FINISHED").
    Action("auto_finish").
    Condition(AmountLessThan{Limit: 1000000})

// Amount < 1000000 时流转成功，否则返回 ErrConditionNotMet
```

### 使用 Hook（生命周期事件）

```go
// 定义 Hook
type NotifyHook struct{}

func (NotifyHook) Before(ctx workflow.Context) error {
    fmt.Println("即将发生流转")
    return nil
}

func (NotifyHook) After(ctx workflow.Context) error {
    fmt.Println("流转完成")
    return nil
}

// 注册流转时附加 Hook
wf.From("APPLY").To("PASSED").
    Action("approve").
    Hook(NotifyHook{})
```

多个 Hook 的执行顺序：

```
h1.Before → h2.Before → 状态流转 → h1.After → h2.After
```

### 组合使用

```go
wf.From("APPLY").To("FINISHED").
    Action("special_pass").
    Guard(AdminGuard{}).
    Condition(AmountLessThan{Limit: 500000}).
    Hook(NotifyHook{}).
    Hook(AuditHook{})
```

执行顺序：

```
AdminGuard.Check → AmountLessThan.Evaluate →
NotifyHook.Before → AuditHook.Before →
状态流转 →
NotifyHook.After → AuditHook.After
```

### 完整审批流程示例

```go
package main

import (
    "fmt"
    "github.com/xiws/orca/pkg/workflow"
)

func main() {
    // 1. 定义流程
    wf := workflow.NewWorkflow("approval")
    wf.State("APPLY")
    wf.State("REJECTED")
    wf.State("PASSED")
    wf.State("FINISHED").Terminal()

    wf.From("APPLY").To("PASSED").Action("approve")
    wf.From("APPLY").To("REJECTED").Action("reject")
    wf.From("REJECTED").To("APPLY").Action("resubmit")
    wf.From("PASSED").To("FINISHED").Action("finish")

    // 2. 创建引擎
    engine := workflow.NewEngine(wf)

    // 3. 模拟审批流程
    order := &Order{ID: "10001", Status: "APPLY"}

    // 驳回
    result, err := engine.Execute(workflow.ExecuteRequest{
        InstanceID:   order.ID,
        CurrentState: order.Status,
        Action:       "reject",
    })
    if err != nil {
        panic(err)
    }
    order.Status = result.To // APPLY -> REJECTED
    fmt.Printf("step1: %s -> %s\n", result.From, result.To)

    // 重新申请
    result, err = engine.Execute(workflow.ExecuteRequest{
        InstanceID:   order.ID,
        CurrentState: order.Status,
        Action:       "resubmit",
    })
    if err != nil {
        panic(err)
    }
    order.Status = result.To // REJECTED -> APPLY
    fmt.Printf("step2: %s -> %s\n", result.From, result.To)

    // 通过
    result, err = engine.Execute(workflow.ExecuteRequest{
        InstanceID:   order.ID,
        CurrentState: order.Status,
        Action:       "approve",
    })
    if err != nil {
        panic(err)
    }
    order.Status = result.To // APPLY -> PASSED
    fmt.Printf("step3: %s -> %s\n", result.From, result.To)

    // 完成
    result, err = engine.Execute(workflow.ExecuteRequest{
        InstanceID:   order.ID,
        CurrentState: order.Status,
        Action:       "finish",
    })
    if err != nil {
        panic(err)
    }
    order.Status = result.To // PASSED -> FINISHED
    fmt.Printf("step4: %s -> %s\n", result.From, result.To)
}

type Order struct {
    ID     string
    Status string
}
```

输出：

```
step1: APPLY -> REJECTED
step2: REJECTED -> APPLY
step3: APPLY -> PASSED
step4: PASSED -> FINISHED
```

### 错误处理

```go
result, err := engine.Execute(workflow.ExecuteRequest{
    CurrentState: "APPLY",
    Action:       "unknown_action",
})

switch {
case errors.Is(err, workflow.ErrStateNotFound):
    // 当前状态不存在
case errors.Is(err, workflow.ErrInvalidTransition):
    // 无匹配的流转
case errors.Is(err, workflow.ErrForbidden):
    // 权限不足
case errors.Is(err, workflow.ErrConditionNotMet):
    // 条件不满足
case err != nil:
    // Hook 执行失败等其他错误
default:
    // 成功：result.From, result.To
}
```

---

## 8b. Engine.Run 自动执行

除了单步 `Execute`，Engine 还提供 `Run` 方法自动驱动整个流程：

### 输入

```go
type RunRequest struct {
    InitialState string  // 初始状态
    Context      Context // 执行上下文
    InstanceID   string  // 实例 ID（可选）
}
```

### 输出

```go
type RunResult struct {
    FinalState string          // 最终状态
    History    []ExecuteResult // 完整流转历史
}
```

### 执行流程

```
RunRequest
      |
      v
  查询当前状态 ─不存在─> ErrStateNotFound
      |
      v
  终态？ ──YES──> 返回 RunResult
      |
     NO
      |
      v
  State.Handler 为 nil？ ──YES──> 配置错误
      |
     NO
      |
      v
  Handler.Handle(ctx) → action
      |
      v
  查找 Transition（From + action）
      |
      v
  Guard → Condition → Hook → 状态流转
      |
      v
  记录到 History，更新当前状态
      |
      v
  循环 ↑
```

### 示例

```go
wf := workflow.NewWorkflow("task-execution")
wf.State("execute").Handler(ExecuteHandler{})
wf.State("verify").Handler(VerifyHandler{})
wf.State("done").Terminal()

wf.From("execute").To("verify").Action("complete")
wf.From("verify").To("done").Action("complete")
wf.From("verify").To("execute").Action("fail") // 验证失败回退

engine := workflow.NewEngine(wf)
result, err := engine.Run(workflow.RunRequest{
    InitialState: "execute",
    InstanceID:   "task-001",
})
// Engine 自动驱动：execute → verify → done（或 verify → execute → verify → ... → done）
fmt.Printf("最终状态: %s, 流转次数: %d\n", result.FinalState, len(result.History))
```

---

## 13. 设计原则

1. **状态机不保存业务状态**：Execute 只返回结果，业务负责持久化。
2. **Engine 是无状态的**：可安全并发使用，状态由业务维护。
3. **Context 可为 nil**：Engine 内部自动创建空 Context。
4. **TransitionID 可选**：仅当 InstanceID 非空时生成，格式 `{InstanceID}:{From}->{To}`。
5. **重复注册状态不报错**：返回同一 State 的 builder，幂等。

---

## 14. 后续扩展方向

这个 Spec 的核心定位：**不是传统状态机，而是后端可编程 Workflow Engine。**

已实现：
- Handler 节点行为：状态可挂载 Handler，Engine.Run 自动驱动流转
- 完整执行循环：进入状态 → Handler → 查找 transition → Guard/Condition/Hook → 流转 → 重复

后续可以扩展：
- 超时自动流转
- 定时任务
- 人工审批节点
- Saga 补偿
- Agent 执行生命周期管理
