# command

## 目标

实现 agent 可调用的四类基础工具命令：**读（read）/ 写（write）/ 编辑（edit）/ bash**。

LLM 输出的结构化结果被解析成一个命令对象，交给 `pkg/command` 的注册表同步分发执行，执行结果再作为事件发回会话（见 [event_handler.md](./event_handler.md)）。

## 语义约定

| 命令 | 语义 | 说明 |
| --- | --- | --- |
| read | 只读 | 按行返回文件内容，不改动文件 |
| write | 覆盖重建 | 整个文件重写，未指定的内容全部丢弃；文件不存在则创建（含父目录） |
| edit | 精准手术 | 基于 `old_string` / `new_string` 做字符串替换，只改动命中的那一小段，其余部分原封不动 |
| bash | 执行 | 在指定 workdir 下执行 shell 命令，返回 stdout / stderr / exit code |

write 与 edit 的区别：如果让 write 去修改一个已有文件，它会毫不留情地把旧内容全部删掉，换成给定的新内容；edit 只替换匹配片段，因此**修改已有文件优先用 edit**，只有新建文件或整体重写时才用 write。

## 协议格式

统一信封：`command` 指明命令名，其余字段为该命令的参数；复杂参数可以包在 `data` 中，也可以直接平铺，`handler.Parse` 两种都接受（不同模型的嵌套习惯不一致，不在入口再做归一化）。`id` 是 int64（雪花 id），可以放在外层也可以放在 `data` 里，外层优先；两处都没有、或给的不是数字（例如 OpenAI 的 `"call-1"`）时生成一个，保证每个结果都能对应回发起它的调用。

```jsonc
{ "command": "read",  "id": 1, "filename": "/home/xiw/test.md", "start": 1, "end": 200 }

{ "command": "write", "id": 2, "data": { "filename": "/home/xiw/test.md", "content": "hello orca" } }

{ "command": "edit",  "id": 3, "data": { "filename": "/home/xiw/test.md",
                                "contents": [{ "old_string": "foo", "new_string": "bar", "replace_all": false }] } }

{ "command": "bash",  "id": 4, "data": { "content": "ls -a", "workdir": "/home/xiw", "timeout": 60 } }
```

一轮可以返回多个命令，既可以是单个对象，也可以是对象数组（`handler.ParseAll`）。模型以 tool call 形式输出时，名字与参数分开到达，用 `handler.OptionFromCall(id int64, name, arguments string)` 拼回同一个信封再解析；tool call 的 id 是字符串，带不进来，传 0 即可由 `Parse` 生成一个。

### edit 的定位方式

`contents` 是一组按顺序应用的替换片段，每个片段支持两种定位方式：

1. `old_string` / `new_string`（**推荐、默认**）：在文件中精确匹配 `old_string` 并替换为 `new_string`。要求在当前文件内唯一命中，命中 0 次或多次且未设置 `replace_all` 时返回错误，不做任何修改。
2. `start` / `end`（可选、行号区间）：按 1-based 闭区间替换行，`content` 为该区间的新内容；仅用于调用方已明确掌握行号的场景。

同一个片段两者同时出现时，以 `old_string` 为准，`start` / `end` 只用于结果校验（行号对不上则报错）。多个片段按数组顺序串行应用，前一个的结果作为后一个的输入；任一片段失败则整条 edit 命令失败且不落盘（先在内存中算完整内容，再一次性写入）。

## 返回值

`CommandHandler.Handle(cmd CommandOption) (error, any)`：Go 层错误与业务结果分离。

- 第一个返回值只表达**框架级**错误（未注册、参数为 nil 等）。
- 业务级结果统一放进第二个返回值，便于序列化后回传给 LLM：

```go
// CommandResult 是所有命令的统一返回结构，定义在 internal/handler/handler.go。
type CommandResult struct {
    Id      int64  `json:"id"`      // 与发起它的命令参数上的 id 一致
    Command string `json:"command"`
    OK      bool   `json:"ok"`
    Content string `json:"content"` // read 的文件内容 / bash 的 stdout+stderr / write、edit 的结果描述
    Err     string `json:"err,omitempty"`
}
```

失败时 Content 依然保留：bash 超时或非 0 退出时，已经产生的输出要一并回给模型，否则它无从判断。

| 命令 | Content 内容 | OK=false 的典型 Err |
| --- | --- | --- |
| read | 带行号的文件内容，如 `12\tfoo` | 文件不存在、无权限、超出允许目录 |
| write | `wrote 1024 bytes to /home/xiw/test.md` | 创建目录失败、只读路径 |
| edit | 每个片段的替换摘要 + 变更行数 | `old_string` 未命中 / 命中多次、区间越界 |
| bash | stdout 与 stderr 合并输出 | 命令不存在、退出码非 0、超时 |

## Go 结构定义

代码位置：

| 文件 | 内容 |
| --- | --- |
| `internal/handler/handler.go` | 命令名常量、哨兵错误、`CommandResult`、`Register` |
| `internal/handler/option.go` | 四类命令的参数与构造函数 |
| `internal/handler/workspace.go` | `Workspace.Resolve`：路径解析与越界检查 |
| `internal/handler/fileio.go` | 原子写、读文件、按行切分 |
| `internal/handler/{read,write,edit,bash}.go` | 四个 `CommandHandler` |
| `internal/handler/proc_{unix,windows}.go` | 进程组创建与超时 kill |
| `internal/handler/parse.go` | JSON 协议 → `CommandOption` |
| `internal/handler/schema.go` | 提示词里的工具说明 |
| `internal/agent/agent_runtime.go` | 命令执行与事件发布的串联 |

每个参数实现 `command.CommandOption`：`GetName()` 返回常量命令名（零值对象也能正确路由），`GetId()` 返回 int64 的调用 id。

```go
type ReadOption struct {
    Id       int64 // 调用 id，缺省时由 Parse 补一个雪花 id（utils.GetSnowFlakeId）
    Filename string
    Start    int // 0 表示从头开始
    End      int // 0 表示读到结尾
}

type WriteOption struct {
    Id       int64
    Filename string
    Content  string
}

type EditOption struct {
    Id       int64
    Filename string
    Contents []EditFragment
}

type EditFragment struct {
    OldString  string `json:"old_string"`
    NewString  string `json:"new_string"`
    ReplaceAll bool   `json:"replace_all,omitempty"`
    Start      int    `json:"start,omitempty"` // 可选，行号定位
    End        int    `json:"end,omitempty"`
    Content    string `json:"content,omitempty"`
}

type BashOption struct {
    Id      int64
    Content string
    Workdir string
    Timeout int // 秒，0 取 DefaultBashTimeout
}
```
参数一律以指针形式交给 handler（`Parse` 与各 `NewXxxOption` 产出的都是指针）。

注册与执行，见 `handler.Register`：

```go
handle := command.NewCommandHandle()
if err := handler.Register(handle, handler.Workspace{Root: projectDir}); err != nil {
    return err
}

err, value := handle.Execute(handler.NewReadOption(1, "docs/command.md", 1, 40))
result := value.(handler.CommandResult)
```

## 执行流程

1. `internal/llm` 流式输出结束，得到 `llm.Result`（可能包含多个 `ToolCall`）
2. `agent.Runtime.RunToolCalls(result)` 把每个调用解成 `CommandOption`，按顺序 `CommandHandle.Execute` 同步执行；另有 `agent.Runtime.Run(raw)` 直接接受上面的 JSON 协议
3. 执行结果包成 `agent.ResultEvent` `Publish` 到 `event.EventBus`
4. 会话把结果作为新的 prompt 上下文回填，进入下一轮，直到 LLM 不再产出命令

read / write / edit / bash 之间不并发执行：同文件的读改写必须串行，避免 edit 基于过期内容计算。

解析失败、命令未注册都是**业务失败**，产出一个 `OK=false` 的结果而不是中断整轮，否则模型不知道自己哪里写错了。发布事件是 best-effort：没人订阅或总线已关闭不影响结果回传。

## 实现约定

- 文件路径：绝对路径原样使用，相对路径基于 `Workspace.Root`（缺省为进程工作目录）解析；write / edit 写入前自动创建父目录。
- 白名单：四类命令（包括 read 与 bash 的 workdir）都只能碰 workspace 内的路径；比较前把路径与根目录都解引用到真实位置，因此 `..` 和指向外部的符号链接都拦得住。
- 编码与换行：按 UTF-8 处理，不在 read / edit 中做 `\r\n` 转换；行号区间的 edit 会沿用文件原有的换行风格，read 的输出则去掉行尾 `\r`。
- read 分页：单次最多 `MaxReadLines`（2000）行，每行前缀 `N\t`；截断时在末尾给出续读的 `start` / `end`。
- 原子写：write 与 edit 都先写同目录的临时文件再 rename，不产生半截文件；已存在的文件保留原权限位，失败时不留临时文件。
- edit 不落盘的情况：任一片段定位失败则整条命令失败，文件保持原状；算下来内容与原文件相同时不写。
- bash 超时与取消：默认 `DefaultBashTimeout`（60）秒，超时把整个进程组 SIGKILL（Windows 下只能 kill shell 本身），已产生的输出仍会返回。
- bash 输出上限：stdout 与 stderr 合并后最多 `MaxBashOutput`（32KB），超出部分丢弃并在末尾说明丢了多少字节。

## TODO

- [x] `internal/handler` 下实现四个 CommandHandler
- [x] read / write / edit / bash 的单元测试（含 edit 多片段、old_string 命中多次、越界路径等用例）
- [x] bash 输出过大时的截断策略
- [x] 向提示词声明各命令的 JSON schema（`handler.ToolPrompt()`，尚未拼进 `agent.PromptContext`，它还是个空占位结构体）
- [ ] 会话层的多轮循环：把 `CommandResult` 回填成下一轮的 prompt
- [ ] write / edit 的确认策略（哪些路径需要用户批准后才能写）
