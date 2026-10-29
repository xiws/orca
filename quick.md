# quick workflow

> 新人导航：只讲当前真正在跑的架构。

## 1. 一句话架构

CLI 提交任务 → `app.Service` 排队 → `workflow.Runner` 驱动节点 →
`agent.Runner` 执行 Invocation → `providers` 调模型 → `tools.Gateway` 执行工具 →
`store` 更新内存状态、追加历史 → 事件回流终端。

所有状态在进程内存里。进程退出，状态消失；`history.jsonl` 只用来事后翻看。

## 2. 模块（按装配与依赖方向）

- **`cmd/cli`**：参数解析（`args.go`）、提交与跟踪运行（`main.go`）、终端渲染
  （`stream.go`）、等待输入时读 stdin（`main.go` 的 `prompt`）。
- **`internal/bootstrap`**：唯一装配点。按顺序创建 config → store → tools →
  providers → agent.Runner → workflow.Runner → app.Service，并把
  `agentRunner.Sink` 绑到 `service.Publish`。
- **`internal/config`**：加载并合并 `models.json` / `setting.json`（项目级优先于
  用户级），展开 `${ENV}`。
- **`internal/domain`**：领域类型。Session / Task / Run / Node / Invocation /
  Event / Policy / Limits / Budget / Mutation，以及 Run 的状态迁移规则。
- **`internal/store`**：一个文件。内存 map + 互斥锁 + 追加写 `history.jsonl`。
  写入走 `Apply(Mutation)`：先整组校验，再一次性提交，失败不留半截状态。
- **`internal/model`**：模型协议抽象。`Client.Complete(ctx, Request, Sink)`、
  `Message` / `Call` / `Response` / `Cursor`；`Sink func(string)` 是流式回调。
- **`internal/providers`**：`model.Client` 的实现。OpenAI 兼容走 SSE，
  otter 走平台后端；凭据只在运行时解析，不落库。
- **`internal/tools`**：工具网关。`gateway.go` 负责策略交集、审批、幂等重放、
  workspace 校验；`registry.go` 是发给模型的 JSON Schema 与运行时校验；
  `files.go` / `bash_unix.go` 做实际 IO。
- **`internal/agent`**：`runner.go` 的 `Advance` 是 Invocation 状态机
  （model → tools → waiting / delegated → completed）；`profile.go` 定义角色
  （executor / responder / reviewer / planner / validator / verifier / repair /
  critic / judge / terminal）的提示词、工具权限和输出契约。
- **`internal/workflow`**：`template.go` 把 `-mode` 翻译成角色序列与策略；
  `runner.go` 的 `Drive → advance → execute` 负责节点调度、并发、委派、修复；
  `evidence.go` 为 verifier 收集验证证据。
- **`internal/app`**：`service.go` 负责任务提交、worker 调度、事件发布/订阅、
  Wait / Respond / Cancel。

## 3. 一次 `orca "..."` 的跳转

```text
cmd/cli/main.go:run
  └─ bootstrap.Open(workspace)                     // 唯一装配点
  └─ Service.Submit
       workflow.Mode(mode) → Template{Roles, Policy}
       建 Session + Task + Run（state=queued）→ store.Apply
       signal() 唤醒 worker
  └─ followRun(run)
       ├─ Subscribe(0) → <-chan domain.Event      // 只收 delta
       └─ 每 100ms readEvents → Observe           // 从内存补齐其它事件
       └─ 状态为 waiting 时：printPrompt → 读 stdin → RespondToInput → 继续

app.Service.work:                                   // 后台 worker
  挑一个可跑的根 Run → workflow.Runner.Drive
    Drive: 循环扫描 run 树 → advance（只写检查点，不调模型）
      advance: freezeInput → readyNodes → 为节点建 Invocation
      execute: 并发批次（信号量 4）→ agent.Runner.Advance
        Advance: model → tools →（create_task → delegated）→ …
          providers.Client.Complete(ctx, req, sink)   // OpenAI SSE / otter
          tools.Gateway.Execute(run, inv, call, key)
            run.Policy ∩ inv.Policy ∩ role.Capabilities → 审批/幂等 → 执行
          store.Apply(Mutation)                       // 状态 + 事件原子落库
    finish / repair / delegate
```

`RespondToInput`、`Cancel` 也不是另一条链路：改完内存状态后同样 `signal()`，
回到 `Service.work` 那个循环。

## 4. 三种 channel 到底指什么

1. **`model.Sink`（回调，不是 channel）**：`providers` 把 SSE 分块逐块交给它。
2. **delta 事件**：`agent.Runner` 把每块包成 `domain.Event{Kind: "delta"}`，通过
   绑定的 `Sink` 调 `Service.Publish`。
3. **订阅通道**：`Service.Subscribe` 返回 `<-chan domain.Event`（容量 64，满了
   直接丢弃），CLI 用它做打字机效果。

真正的事实来源是 `store` 里的 `events`；CLI 每 100ms 用 `Observe` 补齐，
channel 只是实时刷新的旁路。另外 `Service.wake` 只是唤醒 worker 的信号，没有业务语义。

## 5. 读代码顺序

1. `internal/bootstrap/bootstrap.go`（95 行）：所有部件怎么接起来
2. `internal/domain/run.go`：类型与状态机，这是全局约束
3. `internal/agent/runner.go` 的 `Advance`：核心循环
4. `internal/tools/gateway.go` 的 `Execute`：策略 → 审批 → 幂等 → 执行
5. `internal/workflow/runner.go`：节点调度、并发、委派
6. `internal/store/store.go`：`Apply` 的校验与提交
7. `cmd/cli/main.go`：`followRun` 与 stdin 交互

## 6. 两个高频概念

- **Run 树**：`RootRunID` / `ParentRunID` / `Depth`。`create_task` 派生子 Run，
  父 Run 进入 `waiting`，子 Run 完成后把结果作为工具结果续回父 Invocation。
- **Mutation**：一次状态变更的提交单元，包含 Runs / Invocations / Events /
  Inputs / Tools / Delegations 等。`store.Apply` 先校验全部条目，
  任一条失败就整组不写，并把新版本号回写给调用方。

## 7. 明确不做的事

- 不跨进程、不跨崩溃恢复：没有 interrupted / reconciling 状态，没有 resume / reconcile。
- 没有数据库：状态在内存里，历史是 JSONL 追加。
- 外部副作用结果未知时，这一轮直接失败，不重放；工作区改动由 `git status` 兜底。
