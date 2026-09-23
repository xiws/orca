package store

const schemaVersion = 2

// All references are deferred so a Mutation can introduce an entire graph at once.
const schema = `
CREATE TABLE sessions (
 id INTEGER PRIMARY KEY, version INTEGER NOT NULL CHECK(version > 0), data TEXT NOT NULL
);
CREATE TABLE task_ids (id INTEGER PRIMARY KEY);
CREATE TABLE tasks (
 id INTEGER NOT NULL REFERENCES task_ids(id) DEFERRABLE INITIALLY DEFERRED,
 version INTEGER NOT NULL CHECK(version > 0),
 session_id INTEGER REFERENCES sessions(id) DEFERRABLE INITIALLY DEFERRED,
 parent_task_id INTEGER REFERENCES task_ids(id) DEFERRABLE INITIALLY DEFERRED,
 data TEXT NOT NULL, PRIMARY KEY(id, version)
);
CREATE TABLE runs (
 id INTEGER PRIMARY KEY,
 task_id INTEGER NOT NULL, task_version INTEGER NOT NULL,
 session_id INTEGER REFERENCES sessions(id) DEFERRABLE INITIALLY DEFERRED,
 root_run_id INTEGER REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED,
 parent_run_id INTEGER REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED,
 waiting_id INTEGER,
 state TEXT NOT NULL CHECK(state IN ('queued','running','waiting','interrupted','reconciling','cancelling','succeeded','failed','cancelled')),
 version INTEGER NOT NULL CHECK(version > 0), data TEXT NOT NULL,
 FOREIGN KEY(task_id, task_version) REFERENCES tasks(id, version) DEFERRABLE INITIALLY DEFERRED,
 FOREIGN KEY(id, waiting_id) REFERENCES inputs(run_id, id) DEFERRABLE INITIALLY DEFERRED
);
CREATE UNIQUE INDEX one_active_run_per_task ON runs(task_id)
 WHERE state NOT IN ('succeeded','failed','cancelled');
CREATE TABLE invocations (
 id INTEGER PRIMARY KEY,
 run_id INTEGER NOT NULL REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED,
 context_version INTEGER NOT NULL CHECK(context_version >= 0), phase TEXT NOT NULL,
 version INTEGER NOT NULL CHECK(version > 0), data TEXT NOT NULL,
 UNIQUE(run_id, id)
);
CREATE INDEX invocations_by_run ON invocations(run_id, id);
CREATE TABLE inputs (
 id INTEGER PRIMARY KEY,
 run_id INTEGER NOT NULL REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED,
 invocation_id INTEGER NOT NULL,
 call_key TEXT NOT NULL, version INTEGER NOT NULL CHECK(version > 0), data TEXT NOT NULL,
 UNIQUE(run_id, id),
 FOREIGN KEY(run_id, invocation_id) REFERENCES invocations(run_id, id) DEFERRABLE INITIALLY DEFERRED
);
CREATE UNIQUE INDEX inputs_by_call ON inputs(call_key) WHERE call_key <> '';
CREATE TABLE tools (
 key TEXT PRIMARY KEY NOT NULL,
 run_id INTEGER NOT NULL REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED,
 invocation_id INTEGER NOT NULL,
 state TEXT NOT NULL, version INTEGER NOT NULL CHECK(version > 0), data TEXT NOT NULL,
 FOREIGN KEY(run_id, invocation_id) REFERENCES invocations(run_id, id) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX tools_by_run ON tools(run_id, state);
CREATE TABLE delegations (
 key TEXT PRIMARY KEY NOT NULL,
 parent_run_id INTEGER NOT NULL REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED,
 invocation_id INTEGER NOT NULL, data TEXT NOT NULL,
 FOREIGN KEY(parent_run_id, invocation_id) REFERENCES invocations(run_id, id) DEFERRABLE INITIALLY DEFERRED
);
CREATE TABLE delegation_children (
 delegation_key TEXT NOT NULL REFERENCES delegations(key) DEFERRABLE INITIALLY DEFERRED,
 sequence INTEGER NOT NULL,
 child_run_id INTEGER NOT NULL REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED,
 PRIMARY KEY(delegation_key, sequence), UNIQUE(delegation_key, child_run_id)
);
CREATE TABLE artifacts (
 id INTEGER PRIMARY KEY,
 run_id INTEGER NOT NULL REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED,
 invocation_id INTEGER, data TEXT NOT NULL,
 FOREIGN KEY(run_id, invocation_id) REFERENCES invocations(run_id, id) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX artifacts_by_run ON artifacts(run_id, id);
CREATE TABLE events (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 run_id INTEGER NOT NULL REFERENCES runs(id) DEFERRABLE INITIALLY DEFERRED,
 assistant_sequence INTEGER NOT NULL DEFAULT 0 CHECK(assistant_sequence >= 0),
 invocation_id INTEGER, kind TEXT NOT NULL, content TEXT NOT NULL,
 FOREIGN KEY(run_id, invocation_id) REFERENCES invocations(run_id, id) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX events_by_run ON events(run_id, sequence);
CREATE TABLE session_messages (
 session_id INTEGER NOT NULL REFERENCES sessions(id) DEFERRABLE INITIALLY DEFERRED,
 sequence INTEGER NOT NULL CHECK(sequence > 0),
 task_id INTEGER REFERENCES task_ids(id) DEFERRABLE INITIALLY DEFERRED,
 data TEXT NOT NULL, PRIMARY KEY(session_id, sequence)
);
CREATE TABLE invocation_messages (
 invocation_id INTEGER NOT NULL REFERENCES invocations(id) DEFERRABLE INITIALLY DEFERRED,
 context_version INTEGER NOT NULL CHECK(context_version >= 0),
 sequence INTEGER NOT NULL CHECK(sequence > 0),
 data TEXT NOT NULL, PRIMARY KEY(invocation_id, context_version, sequence)
);
PRAGMA user_version = 2;
`
