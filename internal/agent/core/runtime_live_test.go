//go:build live

package core

import (
	"testing"

	"github.com/xiws/orca/internal/llm"
)

func TestRunLiveInvocation(t *testing.T) {
	if testing.Short() {
		t.Skip("live model test")
	}
	runtime, workspace := newTestRuntime(t)
	seedFile(t, workspace, "sample.txt", "live fixture")
	inv := newConversation(workspace.Root, "Read sample.txt and report its content. Do not change any files.")
	inv.Provider = llm.GetProvider("ollama", "ornith-1.5:9b")
	inv.Provider.AllowedTools = []string{"read"}
	if _, err := runtime.Run(inv); err != nil {
		t.Fatal(err)
	}
}
