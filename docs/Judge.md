# Judge mode
对，我觉得这个方向是对的，而且比继续往 `Runtime` 里堆逻辑更有价值。

你现在：

```text
需求
 ↓
Agent Loop
 ↓
Tool
 ↓
Result
 ↓
Agent Loop
```

可以变成：

```text
                    ┌──────────────┐
                    │   Executor   │
                    │  原来的 Agent │
                    └──────┬───────┘
                           │
需求 → Plan/Loop → Tool → Result
                           │
                           ▼
                    ┌──────────────┐
                    │    Judge     │
                    │   裁判 Agent   │
                    └──────┬───────┘
                           │
                    ┌──────┴──────┐
                    │             │
                  PASS           FAIL
                    │             │
                    ▼             ▼
                   完成       返回修复意见
                                  │
                                  ▼
                              Executor
```

### 关键点：Judge 不应该只是“看看最后回答”

我建议它审查的是：

```text
原始需求
+
Agent 已执行的动作
+
Tool Result
+
当前项目最终状态
```

而不是：

```text
需求 + Agent 最终回答
```

否则 Judge 很容易被 Agent 的一句：

> “已经完成了。”

骗过去。

---

## 我会把它设计成独立的 `Judge`

例如：

```text
internal/
├── agent/
│   ├── runtime.go
│   ├── session.go
│   ├── task.go
│   └── ...
│
├── judge/
│   ├── judge.go
│   ├── prompt.go
│   └── result.go
│
└── ...
```

接口大概：

```go
type Judge interface {
    Judge(ctx context.Context, input Input) (Result, error)
}
```

Input：

```go
type Input struct {
    Requirement string
    Task        *agent.Task
    Messages    []llm.Message
}
```

Result：

```go
type Result struct {
    Passed  bool
    Reason  string
    Fixes   []string
}
```

---

## Runtime 就变成

```go
for {
    result := executor.Run(...)

    judgeResult := judge.Judge(...)

    if judgeResult.Passed {
        return result
    }

    task.AppendJudgeFeedback(judgeResult)
}
```

但是这里有一个很重要的设计：

### 不要让 Judge 直接修改代码

Judge 是：

> **只读裁判**

Executor 才是：

> **唯一执行者**

所以：

```text
Executor
  ├── read
  ├── edit
  ├── write
  └── bash

Judge
  └── read / inspect
```

甚至第一版 Judge 可以**不拥有 Tool**，只看已有执行记录。

但如果你真的想判断：

> “测试是否真的通过？”

那么 Judge 最终还是需要能够验证状态。

我更推荐：

```text
Judge
  ↓
提出验证动作
  ↓
Verifier / Tool
  ↓
结果
  ↓
Judge
```

例如需求：

> 增加用户注册 API

Executor 说完成了。

Judge：

```text
要求：
1. POST /users 能创建用户
2. 参数校验存在
3. 重复用户返回 409
4. 测试通过
```

于是 Judge 可以要求：

```text
bash go test ./...
```

如果：

```text
FAIL
```

那么：

```text
Judge → FAIL
        ↓
"TestUserRegister failed because..."
        ↓
Executor 修复
```

---

# 我甚至建议再抽象一层

最终不是：

```text
Agent + Judge
```

而是：

```text
Task Runtime
│
├── Executor Agent
│
├── Judge Agent
│
└── Verifier
```

完整闭环：

```text
              ┌─────────────────────┐
              │      Requirement    │
              └──────────┬──────────┘
                         ↓
                 ┌───────────────┐
                 │   Executor    │
                 └───────┬───────┘
                         ↓
                    Tool Calls
                         ↓
                    Tool Results
                         ↓
                 ┌───────────────┐
                 │     Judge     │
                 └───────┬───────┘
                         ↓
                  ┌──────┴──────┐
                  │             │
                PASS           FAIL
                  │             │
                  ↓             ↓
                DONE       Feedback
                                │
                                └────→ Executor
```

这实际上就是一个：

> **Generate → Verify → Repair → Verify**

循环。

我认为这比单纯增加 Planner 更值得你先做。

尤其是你这个 Orca 项目，**Judge 可以成为整个 Agent Runtime 的核心质量保证机制**。
