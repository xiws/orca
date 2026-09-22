package modes

import (
	"testing"

	"github.com/xiws/orca/internal/agent/core"
)

func TestAskMode_Name(t *testing.T) {
	userPrompt := "需求"
	var task = core.NewTask(userPrompt, "")
	runtime := core.NewRuntime()
	var mode Mode = &AskMode{
		Runtime: runtime,
	}

	msg, err := mode.Run(task)
	if err != nil {
		t.Error(err)
	}

	println(msg)
}

func TestChatMode(t *testing.T) {

}
