package handler

// Command names accepted on the wire, exposed so the prompt and the parser can
// never drift apart.
func Commands() []string {
	return []string{CommandRead, CommandWrite, CommandEdit, CommandBash, CommandCreateTask}
}
