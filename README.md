# Orca

ai agent harness CLI
 
命令描述：`orca -f ./docs/test.md -f ./README.md "这是agent 信息说明"`

-f 表示用户附加的文件
--system_prompt -sp 覆盖系统提示词，系统提示词位置 ~/.orca/system_prompt.md, {project}/.orca/system_prompt.md 文件
-m --model 模型名称，默认 ~/.orca/setting.json，{project}/.orca/setting.json

设计：

llm package实现对接各种大模型，将模型输出装换成对应的结构体
agent package是核心包，负责实现 session，runtime，loop



核心逻辑：

1. orca -f ./docs/test.md -f ./README.md "实现文档中的需求"
2. 初始化运行时，注册事件和命令
3. 为用户需求创建一个 task 和 初始化session 等
4. 将task 和session 给到运行时
5. 运行时让ai 找出上下文和给出目标，拆分任务，prompt 变成总上下文和任务自身上下文
6. 依次执行各个任务，任务结果给到总上下文
7. 最后执行总上下文
8. 结束循环
