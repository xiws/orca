// 验证证据收集：从 Invocation 中提取工具调用记录并校验 bash 执行状态
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xiws/orca/internal/agent"
	"github.com/xiws/orca/internal/domain"
	"github.com/xiws/orca/internal/model"
)

// recordedCall 记录 Invocation 中的一次工具调用及其结果
type recordedCall struct {
	key     string
	call    model.Call
	result  string
	results int
}

// invocationCalls 从 Invocation 线程消息中提取所有工具调用记录，
// AssistantSequence 包含被丢弃的上下文以保持调用 ID 唯一。
func invocationCalls(inv *domain.Invocation) []recordedCall {
	var calls []recordedCall
	pending := map[string]int{}
	for i, message := range inv.Thread.Messages {
		if message.Role == "assistant" {
			pending = map[string]int{}
			for _, call := range message.ToolCalls {
				pending[call.ID] = len(calls)
				calls = append(calls, recordedCall{key: fmt.Sprintf("%d:%d:%s", inv.ID, inv.Thread.SequenceOffset+i+1, call.ID), call: call})
			}
		}
		if message.Role == "tool" {
			if index, ok := pending[message.ToolCallID]; ok {
				calls[index].result = message.Content
				calls[index].results++
			}
		}
	}
	return calls
}

// validationRecord 记录单次工具调用的验证证据
type validationRecord struct {
	Key        string `json:"key"`
	CallID     string `json:"call_id"`
	Name       string `json:"name"`
	State      string `json:"state"`
	Result     string `json:"result"`
	Successful bool   `json:"successful"`
}

// successfulShell 检查 bash 工具结果是否表示成功执行
func successfulShell(result string) bool {
	var data struct {
		OK       *bool `json:"ok"`
		ExitCode *int  `json:"exit_code"`
	}
	return json.Unmarshal([]byte(result), &data) == nil && data.OK != nil && *data.OK && data.ExitCode != nil && *data.ExitCode == 0
}

// validationEvidence 收集最新 validator 节点的验证证据。
// 不降级到较早的成功 validator，确保验证的时效性。
func (w *Runner) validationEvidence(ctx context.Context, run *domain.Run) ([]validationRecord, bool, error) {
	for i := len(run.Nodes) - 1; i >= 0; i-- {
		node := run.Nodes[i]
		if node.Role != "validator" {
			continue
		}
		// 不使用较早的成功 validator 降级
		if node.State != "completed" || node.InvocationID == 0 {
			return nil, false, nil
		}
		inv, err := w.store.Invocation(ctx, node.InvocationID)
		if err != nil {
			return nil, false, err
		}
		valid := inv.Phase == "completed" && inv.RunID == run.ID
		bash := 0
		var evidence []validationRecord
		for _, call := range invocationCalls(inv) {
			if call.call.Name == "bash" {
				bash++
			}
			record, err := w.store.Tool(ctx, call.key)
			if errors.Is(err, domain.ErrNotFound) {
				if call.call.Name == "bash" {
					valid = false
				}
				continue
			}
			if err != nil {
				return nil, false, err
			}
			bound := record.Key == call.key && record.InvocationID == inv.ID && record.RunID == run.ID && record.Name == call.call.Name && record.Workspace == run.Workspace
			if !bound {
				if call.call.Name == "bash" {
					valid = false
				}
				continue
			}
			success := record.State == "completed" && record.ExitCode == 0 && successfulShell(record.Result) && call.results == 1 && call.result == record.Result
			if call.call.Name == "bash" && !success {
				valid = false
			}
			evidence = append(evidence, validationRecord{record.Key, call.call.ID, record.Name, record.State, record.Result, success})
		}
		return evidence, valid && bash > 0, nil
	}
	return nil, false, nil
}

// hasValidation 检查 Run 是否具有有效的验证证据
func (w *Runner) hasValidation(ctx context.Context, run *domain.Run) (bool, error) {
	_, valid, err := w.validationEvidence(ctx, run)
	return valid, err
}

// validEvidence 检查验证器引用的证据是否合法：每条引用必须匹配已记录的调用，
// passed 状态必须至少引用一次成功的 bash 执行
func validEvidence(v agent.Verification, records []validationRecord) bool {
	ids := map[string]int{}
	for _, record := range records {
		ids[record.CallID]++
	}
	citedShell := false
	for _, entry := range v.Evidence {
		fields := strings.Fields(entry)
		if len(fields) == 0 {
			return false
		}
		found := false
		for _, record := range records {
			if fields[0] == record.Key || (fields[0] == record.CallID && ids[record.CallID] == 1) {
				found = true
				citedShell = citedShell || (record.Name == "bash" && record.Successful)
				break
			}
		}
		if !found {
			return false
		}
	}
	return v.Status != "passed" || citedShell
}
