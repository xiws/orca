package handler

// Commands 返回线路上接受的命令名称，暴露给提示词和解析器，
// 确保两者不会偏离。
func Commands() []string {
	return []string{CommandRead, CommandWrite, CommandEdit, CommandBash, CommandCreateTask}
}
