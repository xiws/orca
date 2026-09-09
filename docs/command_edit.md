# edit

go get github.com/zbysir/hunkpatch

# hunkpatch

把大模型写出来的、并不严谨的 unified diff 应用到文本上。

[aider][aider] 那套模糊 hunk 匹配算法的 Go 移植，逐个函数与移植来源的 JavaScript
实现做过差分比对。

```go
out, err := hunkpatch.Apply(source, modelProducedDiff)
```

## 要解决的问题

`git apply` 和 `patch` 不收模型给的 diff：行号是编的，上下文是照个大概抄的，缩进会
跑偏，外面还常常裹着模型自己想说的话。下面这些都是模型真实写出来的形态，这个库都能吃：

```diff
@@ -900,3 +900,3 @@          ← 对不上任何位置的行号
-  const t = useTranslations()
+  const t = useTranslations('about')
```

```
*** Begin Patch                ← 用信封代替文件头
*** Update File: config.ts
@@
-const port = 8080
+const port = 3000
*** End Patch
```

```diff
@@                             ← 没有文件头、没有行号、缩进还抄错了
-    <small>今日会员消费</small>
+    <small>本日の会員利用額</small>
```

hunk 一律按内容定位，行号从不参与。

## 安装

```bash
go get github.com/zbysir/hunkpatch
```

需要 Go 1.21+，只有一个依赖：`github.com/sergi/go-diff`。

## 使用

```go
source := "func greet() string {\n\treturn \"hello\"\n}\n"
diff := "@@\n-\treturn \"hello\"\n+\treturn \"hello, world\"\n"

out, err := hunkpatch.Apply(source, diff)
```

`err` 表达三件不同的事，而且这三种情况下返回的文本都是可用的：

| `err`           | 含义                                  |
| --------------- | ------------------------------------- |
| `nil`           | 所有 hunk 都应用了                     |
| `ErrNoHunks`    | diff 里没有 hunk，原样返回 source      |
| `*PartialError` | 一部分应用了，另一部分定位不到          |

真正需要处理的是 `*PartialError`。模型把上下文抄错就长这样，忽略它意味着写回一个
「看起来没问题、实际少改了一处」的文件：

```go
out, err := hunkpatch.Apply(source, diff)

var partial *hunkpatch.PartialError
if errors.As(err, &partial) {
	log.Printf("只应用了 %d/%d 个 hunk", partial.Applied, partial.Total)
	for _, hunk := range partial.Skipped {
		log.Printf("定位不到：\n%s", strings.Join(hunk, ""))
	}
}
```

`ApplyWith` 把同样的信息以 `Result` 返回，并且可以传选项。

## 缩进抄错

模型最常见的失败方式，是代码抄对了、缩进抄错了。`Options{IndentTolerant: true}`
会在所有精确策略都失败之后，再试一次忽略前导空白的匹配，并且**按文件本身的缩进**
（而不是模型写的缩进）来写入替换内容：

```go
res, err := hunkpatch.ApplyWith(source, diff, hunkpatch.Options{IndentTolerant: true})
```

它是最后手段，而且宁可拒绝也不猜：整块必须**唯一命中**，且每一行的缩进必须差同一个
前缀串。忽略缩进会让上下文的区分度下降 —— 同一段内容在两个嵌套层级各出现一次是很
常见的 —— 而这里猜错就是静默写坏文件。完整的护栏清单见 [indent.go](indent.go)。

## 一个 patch 里有多个文件

`Apply` 面向单文件。跨文件的 patch 先拆开：

```go
for _, fd := range hunkpatch.FindDiffs(patch) {
	content := files[fd.NewFileName]
	for _, hunk := range fd.Hunks {
		if next, ok := hunkpatch.ApplyHunk(content, hunk); ok {
			content = next
		}
	}
	files[fd.NewFileName] = content
}
```

## 匹配是怎么做的

没有相似度打分，也没有任何行号运算。hunk 先被拆成「改前」「改后」两段文本，然后拿
改前那段做**精确子串查找**，每种尝试都会连带试一遍去掉空白行的版本。

失败之后依次做两件事：

1. **用文件真实内容把上下文补全。** `makeNewLinesExplicit` 先用 diff-match-patch
   把 hunk 的改前部分对齐到真实文件，再重新生成一份上下文完整的 hunk。模型漏抄或者
   多编了一行上下文，就是靠这一步救回来的。
2. **一行一行地丢掉上下文。** hunk 被切成 (前上下文, 改动, 后上下文) 三元组，然后
   逐步减少上下文重试，前后两侧的各种取舍组合都试一遍，直到能对上。

第二步就是全部的「模糊」之处：匹配本身始终是精确的，容错来自于要求得更少。这也解释
了它的护栏 —— 当剩余上下文去掉空白后不足 10 个字符、而且在文件里出现不止一次时，
这个 hunk 会被拒绝，而不是随便挑一处应用。

## 来源，以及为什么要移植

算法出自 [aider][aider]（Python，Apache-2.0）。它被移植成了 TypeScript 的 npm 包
[llm-diff-patcher][npm]@0.2.1，本项目则是该包 `aider_port` 目录的 Go 移植。

在 Go 里用这套算法，不移植就得跑 JavaScript：内嵌一个 JS 引擎，把库打包好跟二进制
一起带着，并且承担这个引擎在并发和启动上的开销 —— 而且想改一点匹配行为，还得回到
JavaScript 里改完重新打包。原生实现把这些都去掉了，同时把算法放到了一个能用 Go 阅读、
测试和扩展的地方。

也不能随手换一个 Go 的 diff 库。那些库解决的是**生成** diff 的问题；把一个上下文只有
大致正确的 hunk 放回文件里，是另一个问题，而模型制造的正是后者。而真正必须原样移植、
不能替换的是 jsdiff 里的 Myers 实现 —— 当存在多条最短编辑路径时，返回哪一条取决于
对角线的遍历顺序和 addPath/removePath 的取舍规则。不同实现给出的增删分组不一样，
而 `unidiff.formatLines` 会把这个分组直接变成 hunk 文本。换一个库，输出就变了。

### 移植原则：先忠实，再改进

移植保留了原实现的死代码和残缺部分，并逐处用注释标出：

- `searchAndReplace` 算了两个归一化字符串，然后从来不用。
- `allPreprocs` 有四组，但 `tryStrategy` 只实现了三个 flag 里的第一个，于是第 3、4 组
  其实是第 1、2 组的重复，白试两遍。
- `relativeIndent` 这个 flag 解构了就扔掉，也就是说 aider 的 `RelativeIndenter`
  根本没有被移植到 JavaScript。这正是缩进抄错会失败的根因，也是这里加
  `IndentTolerant` 的原因 —— 做成可选项，好让「我们改了行为」永远不会和
  「我们移植错了」混在一起。

## 是怎么验证的

等价性不是声明，是记录下来的。移植过程是对着真实的 llm-diff-patcher bundle（跑在 JS
引擎里）做的，下面每一批用例连同 JavaScript 侧的答案都提交在 `testdata/`：

| 文件                    | 压住的东西                              | 用例数 |
| ----------------------- | --------------------------------------- | ------ |
| `parity_apply.json`     | 完整流程，端到端                         | 409    |
| `parity_internal.json`  | 7 个内部函数，逐个输入                   | 29×7   |
| `parity_unidiff.json`   | jsdiff 的 Myers + unidiff 的 hunk 格式化 | 56     |
| `parity_finddiffs.json` | patch 拆分，含文件名                     | 10     |
| `js_semantics.json`     | 被模拟的那些 JavaScript 字符串原语        | 187    |

**只比最终输出是不够的。** fallback 链兜得太厉害，内部实现改错了，最终输出往往还是
一样的。这是实测出来的，不是猜的：故意把移植改坏 15 处，只比端到端能抓到 8 处。
UTF-16 长度算错、空串当成功、preprocs 少试几组、Myers 的删加互换、tokenize 少 pop
一次，全都被 fallback 掩盖了。所以才有逐函数的 golden。

**对着两边源码读一遍同样不够。** 真正会让 JavaScript 移植翻车的东西都不体现在结构上：

- `String.replace` 传字符串时**只替换第一处**。
- `.length` 按 **UTF-16 码元**算。`"今日会员消费"` 在 JavaScript 里是 6，按字节是
  18 —— 而 `directlyApplyHunk` 里有一条 `length < 10` 的分支。
- 空字符串是 **falsy**，所以返回 `""` 的策略会被当成失败，继续往下试。
- `String.prototype.trim` 认 U+FEFF 是空白、不认 U+0085；Go 的 `strings.TrimSpace`
  在这两个字符上的判断正好相反。

以上每一条都有一个用例，而且都是变异测试证明了其他用例区分不出来才补的。
`js_semantics.json` 由 node 直接跑这些原语生成（`node testdata/js_semantics.mjs`），
所以这批预期值来自真正的 JavaScript 引擎，而不是谁对规范的记忆 —— 重新生成它只需要
node。`parity_*.json` 则是从原 bundle 捕获后原样提交的，要复现需要
llm-diff-patcher@0.2.1 和一个 JS 运行时。

目前 549 个用例，语句覆盖率 96%。

## 有意与原实现不同的地方

1. **`Options{IndentTolerant}`** —— 新增行为，默认关闭，见上文。
2. **`makeNewLinesExplicit` 遇到畸形输入时。** JavaScript 那边会 throw（相邻两段
   类型相同走到 `checkAndAssignTypes`），异常一路冒到 `applyHunks` 之外，整次调用
   就废了。Go 这边没有异常，于是退回未重写的原 hunk 继续跑。这是算法本身唯一的行为
   差异。
3. **部分应用会上报。** 原实现遇到放不下的 hunk 就默默跳过，然后像没事一样把文本
   返回。这里的算法也一样 —— 但 `Apply` 会通过 `*PartialError` 告诉你。相关地，
   完全没有上下文行的 hunk 在上游会「成功」但什么都没改，`ApplyWith` 只有在文本
   真的变了时才计入 applied。
4. **没有移植 `normalizeHunk`。** 应用路径根本走不到它，所以它从来没被差分测试覆盖过；
   而且它的输出行不带结尾换行，结果没法再喂回 `ApplyHunk`。把它导出出去等于给调用方
   埋坑。
5. **diff-match-patch 的超时。** 两边都是 5 秒，超时后库会退化成粗糙 diff。Go 原生
   比同样的代码跑在 JS 引擎里快得多，所以输入足够大时，JavaScript 侧可能已经超时、
   Go 侧还没有。差分用例刻意避开了那个量级。
