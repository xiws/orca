# Orca 文档索引

本目录同时保留了当前说明、早期实现记录和未落地的设计稿。**读任何一篇前先看它
顶部或下表中的状态**，不要默认 docs 里的代码片段就是现在跑的代码。

状态含义：

- **现状**：与当前代码一致，可以直接依赖。
- **部分过时**：主体思路可用，细节以代码为准。
- **历史实现**：描述早期链路（`internal/llm` / `internal/handler` /
  `internal/agent/core` 等），当前主链路不执行这套代码。
- **设计稿**：设想或独立库手册，未被主链路接入。
- **构想 / 草稿**：产品讨论或一句需求，未实现。

## 当前可用

- `usage_examples.md` —— **现状** —— CLI 使用示例（模式、Provider、会话、run 子命令）。
- `test.md` —— **部分过时** —— `cmd/cli` 参数解析与入口；`CliArgs` 已新增
  `Mode` / `SessionId` / `SubCommand` / `SubArgs` / `After`，以 `cmd/cli/args.go` 为准。

## 历史实现

- `llm.md` —— **历史实现** —— 早期 `internal/llm` 设计笔记
  （`Request(..., msgs chan)`）；当前走 `internal/providers` 的
  `Client.Complete(ctx, req, Sink)`。
- `otter.md` —— **历史实现** —— 对应早期 `internal/llm/otter.go`；当前适配在
  `internal/providers/otter.go`，配置示例仍可参考。
- `command.md` —— **历史实现** —— `pkg/command` + `internal/handler` 的文本命令
  协议；当前工具执行走 `internal/tools/gateway.go`。
- `command_edit.md` —— **历史实现** —— hunkpatch 移植记录；hunkpatch 仍被
  `internal/handler/edit.go` 使用，但该 handler 不在主链路，当前 `edit` 工具用
  `old_string` / `new_string`。
- `event_handler.md` —— **历史实现** —— Command/EventBus 接口草稿；当前主链路事件
  是 `domain.Event` + `store.events`。
- `event_display_spec.md` —— **历史实现** —— 针对旧 `ToolBeforeEvent` /
  `ToolAfterEvent` 渲染链路的改造 spec；当前渲染见 `cmd/cli/stream.go`。

## 设计稿

- `workflow.md` —— **设计稿** —— Agent 驱动状态机设想（`pkg/workflow` 当前零引用）；
  当前编排见 `internal/workflow/runner.go`。
- `machine.md` —— **设计稿** —— `pkg/workflow` 通用状态机引擎 API 手册；该包未被
  主链路引用。
- `mode.md` —— **部分过时** —— 八种模式与角色矩阵仍适用；文中目录结构
  （`internal/agent/workflow/`、`core/task.go` 等）与当前代码不符，模式落地见
  `internal/workflow/template.go` + `internal/agent/profile.go`。

## 构想与草稿

- `Clarifier.md` —— **构想** —— 需求澄清 Agent 的产品讨论，未实现。
- `Judge.md` —— **构想** —— 独立 Judge 角色的设计讨论，未实现。
- `tui.md` —— **草稿** —— 仅记录需求；实际实现见 `cmd/tui/model.go`。

## 当前实现入口

- 仓库根 `README.md`：概念、命令、架构总览。
- 仓库根 `quick.md`：只讲当前主链路的模块与调用顺序。
- `internal/bootstrap/bootstrap.go`：所有子系统的唯一装配点。
