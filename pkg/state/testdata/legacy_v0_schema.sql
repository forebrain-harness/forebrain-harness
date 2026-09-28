PRAGMA journal_mode = WAL;

-- fb_sessions.origin ('' native, 'migrated' imported from another agent's
-- on-disk history) is additive: state.Open reconciles the column onto files
-- older binaries created, so existing rows keep every value and read as
-- native until a migration writes them. Keep column entries free of leading
-- comments — parseColumnDecls reads the name off the first token.
CREATE TABLE IF NOT EXISTS fb_sessions (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  title TEXT,
  updated_at INTEGER NOT NULL,
  parent_session_id TEXT NOT NULL DEFAULT '',
  message_count INTEGER NOT NULL DEFAULT 0,
  prompt_tokens INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  cost REAL NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT 0,
  compact_boundary_id TEXT NOT NULL DEFAULT '',
  initial_window_id TEXT NOT NULL DEFAULT '',
  context_reset_row_id INTEGER NOT NULL DEFAULT 0,
  todos TEXT NOT NULL DEFAULT '',
  memory_mode TEXT NOT NULL DEFAULT 'disabled',
  memory_source TEXT NOT NULL DEFAULT '',
  cwd TEXT NOT NULL DEFAULT '',
  git_branch TEXT NOT NULL DEFAULT '',
  project_id TEXT NOT NULL DEFAULT '',
  origin TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_fb_sessions_updated ON fb_sessions(updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_fb_sessions_agent_updated ON fb_sessions(agent_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_fb_sessions_agent_project ON fb_sessions(agent_id, project_id, updated_at DESC);

-- Projects bind a working directory to the settings that travel with it:
-- instructions the sessions carry, a memory scope, and a resource-access
-- switch. The primary agent is the tenant; the project key is the memory
-- identity of the bound root. Archiving is a soft delete: rows stay, lists
-- hide them.
CREATE TABLE IF NOT EXISTS fb_projects (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  name TEXT NOT NULL,
  icon TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  instructions TEXT NOT NULL DEFAULT '',
  root TEXT NOT NULL,
  project_key TEXT NOT NULL,
  memory_scope TEXT NOT NULL DEFAULT 'shared',
  resource_access INTEGER NOT NULL DEFAULT 1,
  pinned INTEGER NOT NULL DEFAULT 0,
  archived_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_fb_projects_agent_updated ON fb_projects(agent_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_fb_projects_agent_root ON fb_projects(agent_id, root);

CREATE TABLE IF NOT EXISTS fb_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL,
  content TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  message_id TEXT NOT NULL DEFAULT '',
  parts TEXT NOT NULL DEFAULT '[]',
  model TEXT,
  updated_at INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER,
  usage_json TEXT NOT NULL DEFAULT '',
  run_started_at TEXT NOT NULL DEFAULT '',
  run_finished_at TEXT NOT NULL DEFAULT '',
  worked_duration_ms INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT 'transcript',
  tool_step_id TEXT NOT NULL DEFAULT '',
  tool_meta_json TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_fb_messages_session ON fb_messages(session_id);
CREATE INDEX IF NOT EXISTS idx_fb_messages_session_source_created ON fb_messages(session_id, source, created_at, id);
CREATE INDEX IF NOT EXISTS idx_fb_messages_message_id ON fb_messages(message_id);
CREATE INDEX IF NOT EXISTS idx_fb_messages_session_run ON fb_messages(session_id, run_id, id);

-- Surface-specific browsing state is deliberately separate from transcript
-- and model context. Rebuilding history never mutates it, and clearing model
-- context never erases where the user was reading.
CREATE TABLE IF NOT EXISTS fb_session_ui_state (
  session_id TEXT NOT NULL,
  surface TEXT NOT NULL,
  state_json TEXT NOT NULL DEFAULT '{}',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (session_id, surface)
);

CREATE TRIGGER IF NOT EXISTS fb_tui_messages_touch_session_insert
AFTER INSERT ON fb_messages
WHEN IFNULL(NEW.source, 'transcript') = 'tui'
BEGIN
  INSERT OR IGNORE INTO fb_sessions(id, title, updated_at, created_at)
  VALUES(NEW.session_id, NEW.session_id, COALESCE(NULLIF(NEW.updated_at, 0), NEW.created_at), NEW.created_at);
  UPDATE fb_sessions
  SET updated_at = MAX(updated_at, COALESCE(NULLIF(NEW.updated_at, 0), NEW.created_at)),
      created_at = CASE
        WHEN created_at <= 0 THEN NEW.created_at
        WHEN NEW.created_at > 0 AND NEW.created_at < created_at THEN NEW.created_at
        ELSE created_at
      END
  WHERE id = NEW.session_id;
END;

CREATE TRIGGER IF NOT EXISTS fb_tui_messages_touch_session_update
AFTER UPDATE ON fb_messages
WHEN IFNULL(NEW.source, 'transcript') = 'tui'
BEGIN
  UPDATE fb_sessions
  SET updated_at = MAX(updated_at, COALESCE(NULLIF(NEW.updated_at, 0), NEW.created_at))
  WHERE id = NEW.session_id;
END;

-- Prompt state frozen for the life of a session. Content injected ahead of the
-- conversation (the skills catalog, the memory instruction) must stay
-- byte-identical for every request of a session or the provider's cached prefix
-- is re-billed from the first token, so the first render is kept here and
-- replayed. It lives with the session rather than in process memory for two
-- reasons: a resumed session gets the same bytes back after a restart, and the
-- value is released when the session it belongs to is, instead of accumulating
-- for the life of a long-running gateway.
CREATE TABLE IF NOT EXISTS fb_session_prompt_state (
  session_id TEXT NOT NULL,
  key TEXT NOT NULL,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (session_id, key)
);

CREATE TABLE IF NOT EXISTS fb_work_items (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  idx INTEGER NOT NULL DEFAULT 0,
  title TEXT,
  state TEXT NOT NULL,
  claimed_by TEXT,
  lease_until INTEGER NOT NULL DEFAULT 0,
  claim_attempts INTEGER NOT NULL DEFAULT 0,
  input_json TEXT,
  output_path TEXT,
  log_path TEXT,
  progress REAL NOT NULL DEFAULT 0,
  started_at INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER NOT NULL DEFAULT 0,
  error TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_fb_work_items_run ON fb_work_items(run_id, idx);
CREATE INDEX IF NOT EXISTS idx_fb_work_items_state ON fb_work_items(state);
CREATE INDEX IF NOT EXISTS idx_fb_work_items_lease ON fb_work_items(lease_until);

CREATE TABLE IF NOT EXISTS fb_actions (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  status TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  answer_json TEXT,
  error TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_fb_actions_status ON fb_actions(status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_fb_actions_kind ON fb_actions(kind, updated_at DESC);

CREATE TABLE IF NOT EXISTS fb_runs (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  input_text TEXT NOT NULL,
  status TEXT NOT NULL,
  token_estimate INTEGER NOT NULL DEFAULT 0,
  usage_prompt_tokens INTEGER NOT NULL DEFAULT 0,
  usage_completion_tokens INTEGER NOT NULL DEFAULT 0,
  usage_cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  usage_cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
  usage_llm_calls INTEGER NOT NULL DEFAULT 0,
  parent_run_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_fb_runs_session ON fb_runs(session_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_fb_runs_status ON fb_runs(status, updated_at DESC);

CREATE TABLE IF NOT EXISTS fb_run_steps (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL,
  seq INTEGER NOT NULL,
  event_type TEXT NOT NULL,
  payload_json TEXT NOT NULL,
  created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_fb_run_steps_run ON fb_run_steps(run_id, seq);

-- Immutable UI/event history for a complete conversation. Its global row id
-- is the session subscription cursor; unlike run-local steps it orders parent,
-- child and background-agent events without relying on wall-clock ties.
CREATE TABLE IF NOT EXISTS fb_session_events (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  event_id TEXT NOT NULL,
  run_id TEXT NOT NULL DEFAULT '',
  event_type TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}',
  occurred_at_ms INTEGER NOT NULL,
  schema_version INTEGER NOT NULL DEFAULT 1,
  UNIQUE(session_id, event_id)
);

CREATE INDEX IF NOT EXISTS idx_fb_session_events_session_sequence
  ON fb_session_events(session_id, sequence);
CREATE INDEX IF NOT EXISTS idx_fb_session_events_run_sequence
  ON fb_session_events(run_id, sequence);
CREATE INDEX IF NOT EXISTS idx_fb_session_events_session_type_sequence
  ON fb_session_events(session_id, event_type, sequence);

CREATE TABLE IF NOT EXISTS fb_run_waits (
  run_id TEXT NOT NULL,
  action_id TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  tool_input_json TEXT NOT NULL,
  session_snapshot_json TEXT NOT NULL DEFAULT '',
  source_json TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (run_id, action_id)
);

CREATE INDEX IF NOT EXISTS idx_fb_run_waits_action ON fb_run_waits(action_id);

CREATE TABLE IF NOT EXISTS fb_files (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  project_id TEXT NOT NULL DEFAULT '',
  run_id TEXT NOT NULL DEFAULT '',
  original_name TEXT NOT NULL,
  media_type TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  storage_backend TEXT NOT NULL,
  storage_relpath TEXT NOT NULL DEFAULT '',
  oss_bucket TEXT NOT NULL DEFAULT '',
  oss_key TEXT NOT NULL DEFAULT '',
  oss_etag TEXT NOT NULL DEFAULT '',
  oss_version_id TEXT NOT NULL DEFAULT '',
  oss_endpoint TEXT NOT NULL DEFAULT '',
  parse_status TEXT NOT NULL,
  parsed_text_backend TEXT NOT NULL DEFAULT 'local',
  parsed_text_relpath TEXT NOT NULL DEFAULT '',
  parsed_text_oss_bucket TEXT NOT NULL DEFAULT '',
  parsed_text_oss_key TEXT NOT NULL DEFAULT '',
  parsed_text_oss_etag TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  path TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'attachment'
);

CREATE INDEX IF NOT EXISTS idx_fb_files_session ON fb_files(session_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_fb_files_project ON fb_files(project_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_fb_files_sha256 ON fb_files(sha256);
CREATE INDEX IF NOT EXISTS idx_fb_files_parse_status ON fb_files(parse_status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_fb_files_storage_backend ON fb_files(storage_backend, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_fb_files_oss_key ON fb_files(oss_key);
CREATE INDEX IF NOT EXISTS idx_fb_files_source_path_created ON fb_files(source, path, created_at DESC, id);
CREATE INDEX IF NOT EXISTS idx_fb_files_session_source_path_created ON fb_files(session_id, source, path, created_at DESC, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_fb_files_workspace_unique
  ON fb_files(session_id, path, version)
  WHERE source='workspace_file';

CREATE TABLE IF NOT EXISTS fb_todos (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL,
  content TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'todo',
  priority TEXT NOT NULL DEFAULT 'medium',
  created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
  updated_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
  FOREIGN KEY(session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_fb_todos_session_id ON fb_todos(session_id);
CREATE INDEX IF NOT EXISTS idx_fb_todos_status ON fb_todos(status);

-- project_key is the project scope (memories.ProjectKey) the thread this row
-- was extracted from was checked out in. It is required, not backfilled: a
-- thread with no resolvable project (a blank cwd — the gateway/channel case
-- with no launch directory) is never claimed for stage-1 extraction in the
-- first place, so every row here always has a scope to belong to. This is what
-- keeps phase-2 consolidation for one project from ever reading another
-- project's raw memory.
CREATE TABLE IF NOT EXISTS fb_memory_stage1_outputs (
  thread_id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  project_key TEXT NOT NULL CHECK (TRIM(project_key) <> ''),
  source_updated_at INTEGER NOT NULL,
  raw_memory TEXT NOT NULL,
  rollout_summary TEXT NOT NULL,
  rollout_slug TEXT,
  generated_at INTEGER NOT NULL,
  usage_count INTEGER,
  last_usage INTEGER,
  selected_for_phase2 INTEGER NOT NULL DEFAULT 0,
  selected_for_phase2_source_updated_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_fb_memory_stage1_agent_project
  ON fb_memory_stage1_outputs(agent_id, project_key, source_updated_at DESC, thread_id DESC);

CREATE TABLE IF NOT EXISTS fb_memory_jobs (
  kind TEXT NOT NULL,
  job_key TEXT NOT NULL,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  status TEXT NOT NULL,
  worker_id TEXT,
  ownership_token TEXT,
  started_at INTEGER,
  finished_at INTEGER,
  lease_until INTEGER,
  retry_at INTEGER,
  retry_remaining INTEGER NOT NULL,
  last_error TEXT,
  input_watermark INTEGER,
  last_success_watermark INTEGER,
  PRIMARY KEY (kind, job_key)
);

CREATE INDEX IF NOT EXISTS idx_fb_memory_jobs_agent_claim
  ON fb_memory_jobs(agent_id, kind, status, retry_at, lease_until);

CREATE TABLE IF NOT EXISTS fb_tool_audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  tool_name TEXT NOT NULL,
  detail_json TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL DEFAULT 0,
  finished_at INTEGER NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_fb_tool_audit_run ON fb_tool_audit(run_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_fb_tool_audit_session ON fb_tool_audit(session_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_fb_tool_audit_source ON fb_tool_audit(session_id, source, id DESC);

CREATE TABLE IF NOT EXISTS fb_intel_audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  reason TEXT NOT NULL,
  delta INTEGER NOT NULL,
  balance_after INTEGER NOT NULL,
  ref_text TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_fb_intel_audit_created ON fb_intel_audit(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_fb_intel_audit_reason ON fb_intel_audit(reason, created_at DESC);

-- Cron jobs are tenant data: each belongs to one primary agent, and only that
-- agent's jobs run while it is the active one. next_run_at is the scheduler's
-- only index — a NULL means paused, which is how a paused job stays out of the
-- due query without a second flag being consulted.
CREATE TABLE IF NOT EXISTS fb_cron_jobs (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  project_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  schedule TEXT NOT NULL,
  prompt TEXT NOT NULL DEFAULT '',
  deliver TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  repeat_limit INTEGER NOT NULL DEFAULT 0,
  run_count INTEGER NOT NULL DEFAULT 0,
  next_run_at INTEGER,
  last_run_at INTEGER NOT NULL DEFAULT 0,
  last_status TEXT NOT NULL DEFAULT '',
  last_error TEXT NOT NULL DEFAULT '',
  last_output TEXT NOT NULL DEFAULT '',
  failure_streak INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_fb_cron_jobs_agent ON fb_cron_jobs(agent_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_fb_cron_jobs_due ON fb_cron_jobs(agent_id, enabled, next_run_at);
CREATE INDEX IF NOT EXISTS idx_fb_cron_jobs_agent_project ON fb_cron_jobs(agent_id, project_id, created_at DESC);

-- One row per fire, so a job's history survives the job record being edited.
CREATE TABLE IF NOT EXISTS fb_cron_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id TEXT NOT NULL,
  agent_id TEXT NOT NULL DEFAULT '',
  run_id TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL DEFAULT '',
  trigger TEXT NOT NULL DEFAULT 'schedule',
  status TEXT NOT NULL DEFAULT 'running',
  output TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  delivered_to TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL,
  finished_at INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_fb_cron_runs_job ON fb_cron_runs(job_id, started_at DESC);

-- A heartbeat belongs to one session, not to the fleet: it is a recurring
-- instruction inside that conversation, so the session id is the key.
CREATE TABLE IF NOT EXISTS fb_heartbeats (
  session_id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL DEFAULT '',
  interval_seconds INTEGER NOT NULL,
  prompt TEXT NOT NULL,
  paused INTEGER NOT NULL DEFAULT 0,
  last_fired_at INTEGER NOT NULL DEFAULT 0,
  next_run_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_fb_heartbeats_due ON fb_heartbeats(paused, next_run_at);

-- The memory search index: one row per line of every file under a memory root,
-- carrying that line already segmented into index terms.
--
-- It is derived data. Every row can be rebuilt from the files themselves, and
-- is, whenever a file's size or mtime moves — the search that needs the index
-- is the same pass that reads the files, so the index is refreshed from the
-- very bytes being searched and cannot answer for a version of a file that is
-- no longer on disk.
--
-- It lives here rather than beside the memory files because a memory root is a
-- git workspace the consolidation pass takes diffs against, and an index file
-- sitting in it would land in that diff.
--
-- terms_revision records which revision of the term-splitting rules wrote the
-- file's rows. Bytes are not the only input the rows are derived from: changing
-- how a line is cut into terms leaves rows spelling terms the query no longer
-- asks for, and a file nobody has touched since would otherwise keep them
-- forever. A row from an older revision is therefore as stale as one whose file
-- changed, and is rebuilt on the next search.
CREATE TABLE IF NOT EXISTS fb_memory_index_files (
  root TEXT NOT NULL,
  path TEXT NOT NULL,
  size INTEGER NOT NULL,
  mtime_unix INTEGER NOT NULL,
  terms_revision INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (root, path)
);

-- terms is the only indexed column: it holds the line's text after Chinese word
-- segmentation, so FTS5's own tokenizer only has to split on the spaces the
-- segmenter left. Everything else is addressing, carried UNINDEXED so a match
-- can be tied back to the file and line it came from, and scored with bm25().
CREATE VIRTUAL TABLE IF NOT EXISTS fb_memory_fts USING fts5(
  terms,
  root UNINDEXED,
  path UNINDEXED,
  line_no UNINDEXED,
  tokenize = 'unicode61 remove_diacritics 2'
);
