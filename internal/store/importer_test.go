package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiws/orca/internal/domain"
)

func importLegacyFixture(workspace string) []byte {
	return []byte(fmt.Sprintf(`{
 "id":77,"title":"legacy title","project_path":%q,"create_time":101,"update_time":199,
 "provider":{"Provider":"old-provider","ModelID":"old-model","APIKey":"provider-api-secret","BaseURL":"private-endpoint-secret","headers":{"Authorization":"provider-header-secret"}},
 "messages":[
  {"id":1,"role":"system","content":"system","create_time":101,"api_key":"message-secret"},
  {"id":2,"role":"assistant","content":"orphan answer"},
  {"id":3,"role":"user","content":"<file path=\"a.txt\">\nattachment body\n</file>\n\ninspect","create_time":103},
  {"id":4,"role":"assistant","content":"intermediate"},
  {"id":5,"role":"assistant","content":"checking","tool_calls":[{"id":"call-1","name":"read","arguments":"{\"path\":\"a.txt\"}","reasoning":"inspect attachment","api_key":"tool-secret"}]},
  {"id":6,"role":"tool","content":"file data","tool_call_id":"call-1"},
  {"id":7,"role":"assistant","content":"draft"},
  {"id":8,"role":"assistant","content":"final","create_time":108},
  {"id":9,"role":"user","content":"followup","create_time":109},
  {"id":10,"role":"assistant","content":"outdated answer"},
  {"id":11,"role":"assistant","tool_calls":[{"id":"call-2","name":"read","arguments":"{}"}]},
  {"id":12,"role":"tool","content":"more data","tool_call_id":"call-2"},
  {"id":13,"role":"assistant","content":"second final","create_time":113},
  {"id":14,"role":"user","content":"unfinished","create_time":114},
  {"id":15,"role":"assistant","tool_calls":[{"id":"call-3","name":"read","arguments":"{}"}]}
 ],
 "total_usage":{"prompt_tokens":70,"completion_tokens":30,"total_tokens":100,"api_key":"usage-secret"},
 "otter_state":{"chat_session_id":"remote-chat","parent_message_id":19,"parent_message_id_str":"remote-parent","delivered":15,"cookie":"state-secret","remote_metadata":{"cursor":"archived-cursor","conversation_metadata":"archived-conversation","api_key":"metadata-secret","unknown":"metadata-unknown-secret"}},
 "unknown":{"authorization":"root-unknown-secret"}
}`, workspace))
}

func importV2Fixture(workspace string) []byte {
	return []byte(fmt.Sprintf(`{
 "version":2,
 "session":{"id":77,"version":9,"title":"v2 title","project_path":%q,"create_time":101,"update_time":199,"api_key":"session-secret","messages":[
  {"id":101,"task_id":201,"role":"user","content":"<file path=\"a.txt\">\nattachment body\n</file>\n\ninspect","create_time":101},
  {"id":102,"task_id":201,"role":"assistant","content":"answer","create_time":102},
  {"id":103,"task_id":202,"role":"user","content":"child input","create_time":103}
 ]},
 "tasks":[
  {"id":201,"version":2,"session_id":77,"input":"<file path=\"a.txt\">\nattachment body\n</file>\n\ninspect","title":"inspection","api_key":"task-secret","specification":{"goal":"understand","requirements":[{"id":"R1","description":"preserve","priority":"high","api_key":"requirement-secret"}],"constraints":["offline"],"acceptance_criteria":["history intact"],"assumptions":["local"],"api_key":"spec-secret"}},
  {"id":202,"session_id":77,"parent_task_id":201,"input":"child input","title":"child"}
 ],
 "invocations":[{
  "id":401,"task_id":201,"project_path":%q,"model":{"provider":"old-provider","model_id":"old-model","APIKey":"model-secret"},
  "messages":[
   {"id":501,"role":"system","content":"system"},
   {"id":502,"role":"user","content":"attachment body"},
   {"id":503,"role":"assistant","content":"checking","tool_calls":[{"id":"call-1","name":"read","arguments":"{}","reasoning":"inspect"},{"id":"call-2","name":"list","arguments":"{}"}]},
   {"id":504,"role":"tool","content":"file","tool_call_id":"call-1"},
   {"id":505,"role":"tool","content":"layout","tool_call_id":"call-2"},
   {"id":506,"role":"assistant","content":"answer"}
  ],
  "total_usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18},
  "otter_state":{"chat_session_id":"v2-remote","delivered":6,"remote_metadata":{"cursor":"v2-cursor","authorization":"v2-metadata-secret"}},
  "cursor":{"data":"runtime-cursor-secret"},
  "children":[{"id":402,"task_id":202,"project_path":%q,"model":{"provider":"child","model_id":"child-model","cookie":"child-secret"},"messages":[{"id":601,"role":"user","content":"child"},{"id":602,"role":"assistant","content":"done"}],"total_usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5},"children":[{"id":403,"task_id":202,"project_path":%q,"model":{"provider":"grandchild","model_id":"grandchild-model"},"messages":[{"id":701,"role":"assistant","content":"grandchild"}],"total_usage":{"total_tokens":2},"otter_state":{"parent_message_id_str":"grandchild-parent","delivered":1}}]}]
 }],
 "legacy":{"model":{"provider":"legacy","model_id":"legacy-model","token":"legacy-model-secret"},"messages":[{"id":801,"role":"user","content":"old question"},{"id":802,"role":"assistant","content":"old answer"}],"total_usage":{"prompt_tokens":4,"completion_tokens":3,"total_tokens":7},"otter_state":{"chat_session_id":"legacy-chat","delivered":2}},
 "unknown":{"api_key":"root-unknown-secret"}
}`, workspace, workspace, workspace, workspace))
}

func writeImportFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0640); err != nil {
		t.Fatal(err)
	}
}

func assertImportSourceUnchanged(t *testing.T, path string, before os.FileInfo, original []byte) {
	t.Helper()
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, original) || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("import changed source content, identity, permissions or mtime: %v", err)
	}
}

func assertImportCounts(t *testing.T, s *Store, counts map[string]int) {
	t.Helper()
	for table, want := range counts {
		var got int
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s count = %d, want %d", table, got, want)
		}
	}
}

func assertNoImportCredentials(t *testing.T, workspace string) {
	t.Helper()
	for _, name := range []string{"state.sqlite3", "state.sqlite3-wal"} {
		data, err := os.ReadFile(filepath.Join(workspace, ".orca", name))
		if errors.Is(err, os.ErrNotExist) && name == "state.sqlite3-wal" {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"-secret", "APIKey", "api_key", "BaseURL", "authorization", "Authorization", "cookie", "\"unknown\""} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatalf("credential or unknown field %q reached %s", secret, name)
			}
		}
	}
}

func TestImportLegacyReadOnlyIdempotenceAndReopen(t *testing.T) {
	s, workspace := testStore(t)
	ctx := context.Background()
	if _, err := s.Archive(ctx, 77); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("archive before first import: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.json")
	original := importLegacyFixture(workspace)
	writeImportFixture(t, path, original)
	if err := os.Chmod(path, 0440); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Unix(1234, 0), time.Unix(1234, 0)); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.json")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	id, err := s.Import(ctx, path, "")
	if err != nil || id <= 0 || id == 77 {
		t.Fatalf("import: %d %v", id, err)
	}
	session, err := s.Session(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := canonicalImportPath(workspace)
	if session.ID != id || session.Version != 1 || session.ProjectPath != canonical || session.Title != "legacy title" || session.CreateTime != 101 || session.UpdateTime != 199 {
		t.Fatalf("imported metadata: %+v", session)
	}
	want := []string{"<file path=\"a.txt\">\nattachment body\n</file>\n\ninspect", "final", "followup", "second final", "unfinished"}
	if len(session.Messages) != len(want) {
		t.Fatalf("timeline count: %d", len(session.Messages))
	}
	ids := make(map[int64]bool)
	for i, message := range session.Messages {
		if message.Content != want[i] || message.ID <= 15 || ids[message.ID] || message.TaskID != 0 {
			t.Fatalf("timeline %d: %+v", i, message)
		}
		ids[message.ID] = true
	}
	archive, err := s.Archive(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(original)
	canonicalSource, _ := canonicalImportPath(path)
	if archive.SourcePath != canonicalSource || archive.SourceID != 77 || archive.SHA256 != hex.EncodeToString(digest[:]) || archive.Version != 0 {
		t.Fatalf("provenance: %+v", archive)
	}
	legacy := archive.Legacy
	if legacy == nil || len(legacy.Messages) != 15 || legacy.TotalUsage != (ArchivedUsage{70, 30, 100}) || legacy.Model != (ArchivedModel{"old-provider", "old-model"}) {
		t.Fatalf("legacy transcript: %+v", legacy)
	}
	call := legacy.Messages[4].ToolCalls[0]
	if call.ID != "call-1" || call.Name != "read" || call.Arguments != `{"path":"a.txt"}` || call.Reasoning != "inspect attachment" || legacy.Messages[5].ToolCallID != call.ID {
		t.Fatalf("tool call not preserved: %+v", call)
	}
	state := legacy.OtterState
	if state == nil || state.ChatSessionID != "remote-chat" || state.ParentMessageID != 19 || state.ParentMessageIDStr != "remote-parent" || state.Delivered != 15 || state.RemoteMetadata == nil || state.RemoteMetadata.Cursor != "archived-cursor" || state.RemoteMetadata.ConversationMetadata != "archived-conversation" {
		t.Fatalf("continuation archive: %+v", state)
	}
	for _, source := range []string{path, alias} {
		again, err := s.Import(ctx, source, workspace)
		if err != nil || again != id {
			t.Fatalf("idempotent import: %d %v", again, err)
		}
	}
	assertImportCounts(t, s, map[string]int{"sessions": 1, "session_messages": 5, "tasks": 0, "imported_sessions": 1, "runs": 0, "invocations": 0, "invocation_messages": 0, "tools": 0, "inputs": 0})
	assertNoImportCredentials(t, workspace)
	assertImportSourceUnchanged(t, path, before, original)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoImportCredentials(t, workspace)
	reopened, err := Open(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.Import(ctx, alias, "")
	if err != nil || again != id {
		t.Fatalf("reopened import: %d %v", again, err)
	}
	stored, err := reopened.Archive(ctx, id)
	if err != nil || !reflect.DeepEqual(stored, archive) {
		t.Fatalf("reopened archive changed: %v", err)
	}
	assertImportSourceUnchanged(t, path, before, original)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("unexpected backup or source removal: %v %v", entries, err)
	}
}

func TestImportV2RemapsGraphAndPreservesArchive(t *testing.T) {
	s, workspace := testStore(t)
	ctx := context.Background()
	// Occupy the source IDs in the live database. Import must not overwrite them.
	originalSession := &domain.Session{ID: 77, Title: "existing", Messages: []domain.Message{{ID: 101, Role: domain.UserMessage, TaskID: 201, Content: "existing"}}}
	mustApply(t, s, domain.Mutation{Sessions: []*domain.Session{originalSession}, Tasks: []*domain.Task{{ID: 201, Version: 1, SessionID: 77}}})
	path := filepath.Join(t.TempDir(), "v2.json")
	data := importV2Fixture(workspace)
	writeImportFixture(t, path, data)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.Import(ctx, path, "")
	if err != nil || id == 77 {
		t.Fatalf("import: %d %v", id, err)
	}
	session, err := s.Session(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := s.Archive(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Version != 2 || archive.SourceID != 77 || archive.Session.ID != 77 || len(archive.Tasks) != 2 || len(session.Messages) != 3 || session.Version != 1 {
		t.Fatalf("v2 metadata or counts changed: %+v", archive)
	}
	tasks, err := readMany[domain.Task](ctx, s.db, "SELECT data FROM tasks WHERE session_id = ? ORDER BY version DESC", id)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("tasks: %+v %v", tasks, err)
	}
	root, child := tasks[0], tasks[1]
	if root.ID == 201 || child.ID == 202 || root.ID == child.ID || root.Version != 2 || child.Version != 1 || child.ParentTaskID != root.ID || root.SessionID != id || child.SessionID != id {
		t.Fatalf("task remapping: %+v", tasks)
	}
	if root.Input != archive.Tasks[0].Input || root.Title != archive.Tasks[0].Title || !reflect.DeepEqual(root.Specification, archive.Tasks[0].Specification) {
		t.Fatalf("task attachment or specification lost: %+v", root)
	}
	for i, message := range session.Messages {
		old := archive.Session.Messages[i]
		if message.ID <= 0 || message.ID == old.ID || message.Content != old.Content || message.Role != old.Role || message.CreateTime != old.CreateTime {
			t.Fatalf("timeline was regenerated or lost attachments: %+v", message)
		}
		wantTask := root.ID
		if i == 2 {
			wantTask = child.ID
		}
		if message.TaskID != wantTask {
			t.Fatalf("timeline task reference: %+v", message)
		}
	}
	var messageCount, callCount, invocationCount int
	var tokens int64
	var walk func([]ArchivedInvocation)
	walk = func(list []ArchivedInvocation) {
		for _, inv := range list {
			invocationCount++
			messageCount += len(inv.Messages)
			tokens += inv.TotalUsage.TotalTokens
			for _, message := range inv.Messages {
				callCount += len(message.ToolCalls)
			}
			walk(inv.Children)
		}
	}
	walk(archive.Invocations)
	messageCount += len(archive.Legacy.Messages)
	tokens += archive.Legacy.TotalUsage.TotalTokens
	if invocationCount != 3 || messageCount != 11 || callCount != 2 || tokens != 32 {
		t.Fatalf("audit counts: invocations=%d messages=%d calls=%d tokens=%d", invocationCount, messageCount, callCount, tokens)
	}
	if archive.Invocations[0].Children[0].Children[0].OtterState.ParentMessageIDStr != "grandchild-parent" || archive.Invocations[0].OtterState.RemoteMetadata.Cursor != "v2-cursor" {
		t.Fatal("recursive archived continuation identifiers lost")
	}
	assertImportCounts(t, s, map[string]int{"sessions": 2, "tasks": 3, "task_ids": 3, "session_messages": 4, "imported_sessions": 1, "runs": 0, "invocations": 0, "invocation_messages": 0, "tools": 0, "inputs": 0})
	existing, err := s.Session(ctx, 77)
	if err != nil || !reflect.DeepEqual(existing, originalSession) {
		t.Fatalf("existing session overwritten: %v", err)
	}
	assertNoImportCredentials(t, workspace)
	assertImportSourceUnchanged(t, path, before, data)
}

func TestImportProvenanceAndConcurrentIdempotence(t *testing.T) {
	s, workspace := testStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	first := filepath.Join(dir, "first.json")
	second := filepath.Join(dir, "second.json")
	data := importV2Fixture(workspace)
	writeImportFixture(t, first, data)
	writeImportFixture(t, second, data)
	ids := make(chan domain.SessionID, 8)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			id, err := s.Import(ctx, first, "")
			ids <- id
			errs <- err
		})
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var firstID domain.SessionID
	for id := range ids {
		if firstID != 0 && firstID != id {
			t.Fatal("concurrent duplicate import")
		}
		firstID = id
	}
	secondID, err := s.Import(ctx, second, "")
	if err != nil || secondID == firstID {
		t.Fatalf("same old ID from different source conflated: %d %v", secondID, err)
	}
	// Simulate an external source edit. A different digest is a separate snapshot.
	changed := bytes.Replace(data, []byte("v2 title"), []byte("edited title"), 1)
	writeImportFixture(t, first, changed)
	thirdID, err := s.Import(ctx, first, "")
	if err != nil || thirdID == firstID || thirdID == secondID {
		t.Fatalf("different digest conflated: %d %v", thirdID, err)
	}
	old, err := s.Archive(ctx, firstID)
	if err != nil || old.Session.Title != "v2 title" {
		t.Fatalf("old snapshot changed: %v", err)
	}
	assertImportCounts(t, s, map[string]int{"sessions": 3, "tasks": 6, "session_messages": 9, "imported_sessions": 3})
}

func TestImportOwnerRules(t *testing.T) {
	s, workspace := testStore(t)
	ctx := context.Background()
	other := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"", ".", other, filepath.Join(other, "missing")} {
		t.Run(fmt.Sprintf("source-%q", project), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "legacy.json")
			data := importLegacyFixture(project)
			writeImportFixture(t, path, data)
			if _, err := s.Import(ctx, path, ""); err == nil || !strings.Contains(err.Error(), "explicit owner") {
				t.Fatalf("ambiguous source silently adopted: %v", err)
			}
			for _, owner := range []string{".", other, filepath.Join(other, "missing")} {
				if _, err := s.Import(ctx, path, owner); err == nil || !strings.Contains(err.Error(), "owner") {
					t.Fatalf("invalid owner %q accepted: %v", owner, err)
				}
			}
			id, err := s.Import(ctx, path, alias)
			if err != nil {
				t.Fatalf("explicit canonical owner: %v", err)
			}
			session, err := s.Session(ctx, id)
			canonical, _ := canonicalImportPath(workspace)
			if err != nil || session.ProjectPath != canonical {
				t.Fatalf("wrong new owner: %+v %v", session, err)
			}
			// A prior import is not permission to skip ownership validation.
			if _, err := s.Import(ctx, path, ""); err == nil {
				t.Fatal("idempotent lookup bypassed ownership validation")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "aliased-source.json")
	writeImportFixture(t, path, importLegacyFixture(alias))
	if _, err := s.Import(ctx, path, ""); err != nil {
		t.Fatalf("same canonical source workspace: %v", err)
	}
	conflicting := bytes.Replace(importV2Fixture(workspace), []byte(fmt.Sprintf(`"project_path":%q`, workspace)), []byte(fmt.Sprintf(`"project_path":%q`, other)), 2)
	// Restore only the session path, leaving a foreign invocation path.
	conflicting = bytes.Replace(conflicting, []byte(fmt.Sprintf(`"project_path":%q`, other)), []byte(fmt.Sprintf(`"project_path":%q`, workspace)), 1)
	writeImportFixture(t, path, conflicting)
	if _, err := s.Import(ctx, path, ""); err == nil || !strings.Contains(err.Error(), "explicit owner") {
		t.Fatalf("conflicting child ownership: %v", err)
	}
	if _, err := s.Import(ctx, path, workspace); err != nil {
		t.Fatalf("explicit reassignment of archive: %v", err)
	}
}

func TestImportRejectsMalformedRecords(t *testing.T) {
	s, workspace := testStore(t)
	ctx := context.Background()
	valid := importV2Fixture(workspace)
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"truncated", []byte(`{"id":`), "JSON"},
		{"trailing", append(append([]byte{}, valid...), []byte(` {}`)...), "trailing"},
		{"null", []byte(`null`), "object"},
		{"array", []byte(`[]`), "object"},
		{"invalid utf8", []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}, "UTF-8"},
		{"duplicate root", []byte(`{"id":77,"id":78}`), "duplicate"},
		{"escaped duplicate", []byte(`{"id":77,"\u0069d":78}`), "duplicate"},
		{"case alias", []byte(`{"id":77,"ID":78}`), "duplicate"},
		{"unicode case alias", []byte(`{"id":77,"tasks":[],"tas\u212As":[]}`), "duplicate"},
		{"duplicate unknown", []byte(`{"id":77,"ignored":{"key":1,"key":2}}`), "duplicate"},
		{"nested duplicate", bytes.Replace(valid, []byte(`"provider":"child"`), []byte(`"provider":"child","Provider":"other"`), 1), "duplicate"},
		{"depth limit", []byte(`{"id":77,"ignored":` + strings.Repeat("[", 130) + "0" + strings.Repeat("]", 130) + "}"), "depth"},
	}
	for _, version := range []string{"0", "1", "3", "null", `"2"`, "2.5"} {
		cases = append(cases, struct {
			name string
			data []byte
			want string
		}{"version " + version, bytes.Replace(valid, []byte(`"version":2`), []byte(`"version":`+version), 1), "version"})
	}
	for _, id := range []string{"0", "-1", "null", "1.5", `"77"`, "9223372036854775808"} {
		cases = append(cases, struct {
			name string
			data []byte
			want string
		}{"session ID " + id, bytes.Replace(valid, []byte(`"id":77`), []byte(`"id":`+id), 1), "import"})
	}
	mutations := []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"missing session", func(r map[string]any) { delete(r, "session") }, "session"},
		{"null task", func(r map[string]any) { r["tasks"].([]any)[0] = nil }, "task ID"},
		{"duplicate task", func(r map[string]any) { r["tasks"].([]any)[1].(map[string]any)["id"] = 201 }, "task ID"},
		{"negative task", func(r map[string]any) { r["tasks"].([]any)[0].(map[string]any)["id"] = -1 }, "task ID"},
		{"foreign session", func(r map[string]any) { r["tasks"].([]any)[0].(map[string]any)["session_id"] = 88 }, "another session"},
		{"negative version", func(r map[string]any) { r["tasks"].([]any)[0].(map[string]any)["version"] = -1 }, "version"},
		{"unknown parent", func(r map[string]any) { r["tasks"].([]any)[0].(map[string]any)["parent_task_id"] = 999 }, "parent"},
		{"self parent", func(r map[string]any) { r["tasks"].([]any)[0].(map[string]any)["parent_task_id"] = 201 }, "cycle"},
		{"parent cycle", func(r map[string]any) { r["tasks"].([]any)[0].(map[string]any)["parent_task_id"] = 202 }, "cycle"},
		{"message ID", func(r map[string]any) {
			r["session"].(map[string]any)["messages"].([]any)[0].(map[string]any)["id"] = 0
		}, "message ID"},
		{"duplicate message ID", func(r map[string]any) {
			r["session"].(map[string]any)["messages"].([]any)[1].(map[string]any)["id"] = 101
		}, "message ID"},
		{"message role", func(r map[string]any) {
			r["session"].(map[string]any)["messages"].([]any)[0].(map[string]any)["role"] = "tool"
		}, "message role"},
		{"message task", func(r map[string]any) {
			r["session"].(map[string]any)["messages"].([]any)[0].(map[string]any)["task_id"] = 999
		}, "unknown task"},
		{"invocation ID", func(r map[string]any) { r["invocations"].([]any)[0].(map[string]any)["id"] = -1 }, "invocation ID"},
		{"duplicate invocation ID", func(r map[string]any) {
			r["invocations"].([]any)[0].(map[string]any)["children"].([]any)[0].(map[string]any)["id"] = 401
		}, "invocation ID"},
		{"invocation task", func(r map[string]any) { r["invocations"].([]any)[0].(map[string]any)["task_id"] = 999 }, "unknown task"},
		{"archive message ID", func(r map[string]any) {
			r["invocations"].([]any)[0].(map[string]any)["messages"].([]any)[0].(map[string]any)["id"] = -1
		}, "message ID"},
		{"archive duplicate message ID", func(r map[string]any) {
			r["invocations"].([]any)[0].(map[string]any)["messages"].([]any)[1].(map[string]any)["id"] = 501
		}, "message ID"},
		{"legacy role", func(r map[string]any) {
			r["legacy"].(map[string]any)["messages"].([]any)[0].(map[string]any)["role"] = "unknown"
		}, "message role"},
		{"child usage", func(r map[string]any) {
			r["invocations"].([]any)[0].(map[string]any)["children"].([]any)[0].(map[string]any)["total_usage"].(map[string]any)["total_tokens"] = -1
		}, "usage"},
		{"bad continuation", func(r map[string]any) { r["legacy"].(map[string]any)["otter_state"].(map[string]any)["delivered"] = -1 }, "continuation"},
		{"bad field type", func(r map[string]any) { r["session"].(map[string]any)["title"] = []any{} }, "invalid v2"},
	}
	for _, mutation := range mutations {
		var record map[string]any
		if err := json.Unmarshal(valid, &record); err != nil {
			t.Fatal(err)
		}
		mutation.edit(record)
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, struct {
			name string
			data []byte
			want string
		}{mutation.name, data, mutation.want})
	}
	path := filepath.Join(t.TempDir(), "invalid.json")
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			writeImportFixture(t, path, test.data)
			if _, err := s.Import(ctx, path, workspace); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want error containing %q, got %v", test.want, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, test.data) {
				t.Fatalf("invalid source modified: %v", err)
			}
		})
	}
	assertImportCounts(t, s, map[string]int{"sessions": 0, "tasks": 0, "session_messages": 0})
	var extension int
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'imported_sessions'").Scan(&extension); err != nil || extension != 0 {
		t.Fatalf("invalid records performed schema writes: %d %v", extension, err)
	}
}

func TestImportAtomicRollbackCancellationAndMissing(t *testing.T) {
	s, workspace := testStore(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v2.json")
	writeImportFixture(t, path, importV2Fixture(workspace))
	// Fail after applyMutation has inserted session, timeline and tasks.
	if _, err := s.db.Exec(importSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_import BEFORE INSERT ON imported_sessions BEGIN SELECT RAISE(ABORT, 'forced archive failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(ctx, path, ""); err == nil || !strings.Contains(err.Error(), "forced archive failure") {
		t.Fatalf("failure injection: %v", err)
	}
	assertImportCounts(t, s, map[string]int{"sessions": 0, "tasks": 0, "task_ids": 0, "session_messages": 0, "imported_sessions": 0})
	if _, err := s.db.Exec("DROP TRIGGER fail_import"); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Import(cancelled, path, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled import: %v", err)
	}
	if _, err := s.Import(ctx, path+".missing", ""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing source: %v", err)
	}
	if _, err := s.Import(ctx, filepath.Dir(path), ""); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("directory source: %v", err)
	}
	assertImportCounts(t, s, map[string]int{"sessions": 0, "imported_sessions": 0})
	id, err := s.Import(ctx, path, "")
	if err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	if _, err := s.Archive(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Archive(ctx, 77); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing archive: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(ctx, path, ""); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("closed import: %v", err)
	}
	if _, err := s.Archive(ctx, id); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("closed archive: %v", err)
	}
}

func TestImportTransactionalSchemaAndDeferredFailure(t *testing.T) {
	s, workspace := testStore(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v2.json")
	writeImportFixture(t, path, importV2Fixture(workspace))
	if _, err := s.db.Exec(`CREATE TRIGGER fail_task BEFORE INSERT ON tasks BEGIN SELECT RAISE(ABORT, 'forced task failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(ctx, path, ""); err == nil || !strings.Contains(err.Error(), "forced task failure") {
		t.Fatalf("task failure injection: %v", err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'imported_sessions'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("extension DDL survived failed mutation: %d %v", count, err)
	}
	assertImportCounts(t, s, map[string]int{"sessions": 0, "tasks": 0, "session_messages": 0, "task_ids": 0})
	if _, err := s.db.Exec("DROP TRIGGER fail_task"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(importSchema); err != nil {
		t.Fatal(err)
	}
	// This trigger succeeds, but its missing task fails deferred FK validation at
	// COMMIT, after the archive and all ordinary import rows have been inserted.
	if _, err := s.db.Exec(`CREATE TRIGGER fail_commit AFTER INSERT ON imported_sessions BEGIN
 INSERT INTO session_messages(session_id, sequence, task_id, data) VALUES(NEW.session_id, 999, -1, '{}'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Import(ctx, path, ""); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("deferred failure injection: %v", err)
	}
	assertImportCounts(t, s, map[string]int{"sessions": 0, "tasks": 0, "session_messages": 0, "task_ids": 0, "imported_sessions": 0})
	if _, err := s.db.Exec("DROP TRIGGER fail_commit"); err != nil {
		t.Fatal(err)
	}
	id, err := s.Import(ctx, path, "")
	if err != nil {
		t.Fatalf("retry after failed commit: %v", err)
	}
	session, err := s.Session(ctx, id)
	if err != nil || session.Version != 1 {
		t.Fatalf("insert revision after retry: %+v %v", session, err)
	}
	assertImportCounts(t, s, map[string]int{"sessions": 1, "tasks": 2, "session_messages": 3, "imported_sessions": 1})
}

func TestImportCanonicalOpenedWorkspaceAndRelativeSource(t *testing.T) {
	workspace := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	s, err := Open(alias)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := filepath.Join(t.TempDir(), "legacy.json")
	writeImportFixture(t, path, importLegacyFixture(alias))
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := s.Import(ctx, relative, workspace)
	if err != nil {
		t.Fatalf("relative source, canonical explicit owner: %v", err)
	}
	again, err := s.Import(ctx, path, "")
	if err != nil || again != id {
		t.Fatalf("relative/absolute source provenance diverged: %d %v", again, err)
	}
	session, err := s.Session(ctx, id)
	canonical, _ := canonicalImportPath(workspace)
	if err != nil || session.ProjectPath != canonical {
		t.Fatalf("owner taken from CWD or source instead of DB: %+v %v", session, err)
	}
}
