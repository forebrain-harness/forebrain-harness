
-- Projects bind a working directory to the settings that travel with it:
-- instructions the sessions carry, a memory scope, and a resource-access
-- switch. The primary agent is the tenant; the project key is the memory
-- identity of the bound root. Archiving is a soft delete: rows stay, lists
-- hide them; archived_at NULL means the project is live.
CREATE TABLE fb_projects (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  name TEXT NOT NULL,
  icon TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  instructions TEXT NOT NULL DEFAULT '',
  root TEXT NOT NULL,
  project_key TEXT NOT NULL,
  memory_scope TEXT NOT NULL DEFAULT 'shared' CHECK (memory_scope IN ('shared', 'project_only')),
  resource_access INTEGER NOT NULL DEFAULT 1 CHECK (resource_access IN (0, 1)),
  pinned INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1)),
  archived_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_fb_projects_agent_updated ON fb_projects(agent_id, updated_at DESC);
CREATE INDEX idx_fb_projects_agent_root ON fb_projects(agent_id, root);

-- The compact/context-reset pointers reference fb_messages rows, and
-- fb_messages references this table back; SQLite checks foreign keys at DML
-- time, not at create time, so the cycle is declarable.
CREATE TABLE fb_sessions (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  -- A session is born titled with its own id ("unnamed") and is named from
  -- its first visible user message by an atomic conditional update.
  title TEXT NOT NULL CHECK (TRIM(title) <> ''),
  parent_session_id TEXT REFERENCES fb_sessions(id) ON DELETE SET NULL,
  project_id TEXT REFERENCES fb_projects(id) ON DELETE SET NULL,
  origin TEXT NOT NULL DEFAULT 'native' CHECK (origin IN ('native', 'migrated')),
  -- source marks what a session is FOR ('' = an ordinary conversation,
  -- 'workshop' = a skill-workshop task): the chat drawer filters on it so a
  -- workshop task stays out of the conversation list.
  source TEXT NOT NULL DEFAULT '',
  cwd TEXT NOT NULL DEFAULT '',
  git_branch TEXT NOT NULL DEFAULT '',
  memory_mode TEXT NOT NULL DEFAULT 'disabled',
  memory_source TEXT NOT NULL DEFAULT '',
  initial_window_id TEXT NOT NULL DEFAULT '',
  compact_boundary_message_id INTEGER REFERENCES fb_messages(id) ON DELETE SET NULL,
  context_reset_message_id INTEGER REFERENCES fb_messages(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL CHECK (created_at > 0),
  updated_at INTEGER NOT NULL CHECK (updated_at > 0),
  UNIQUE (id, agent_id)
) STRICT;
CREATE INDEX idx_fb_sessions_agent_updated ON fb_sessions(agent_id, updated_at DESC);
CREATE INDEX idx_fb_sessions_agent_project ON fb_sessions(agent_id, project_id, updated_at DESC);
CREATE INDEX idx_fb_sessions_parent ON fb_sessions(parent_session_id) WHERE parent_session_id IS NOT NULL;


-- The stored transcript. visibility hides a row from display and from the
-- next model request while keeping it on disk: 'repaired' marks a mechanical
-- transcript repair, 'withdrawn' a message the user deliberately took back.
CREATE TABLE fb_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  run_id TEXT REFERENCES fb_runs(id) ON DELETE SET NULL,
  role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool', 'reasoning', 'system')),
  visibility TEXT NOT NULL DEFAULT 'visible' CHECK (visibility IN ('visible', 'repaired', 'withdrawn')),
  content TEXT NOT NULL,
  parts TEXT NOT NULL DEFAULT '[]',
  message_id TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  usage_json TEXT NOT NULL DEFAULT '',
  tool_step_id TEXT NOT NULL DEFAULT '',
  tool_meta_json TEXT NOT NULL DEFAULT '',
  exec_started_at_ms INTEGER,
  exec_finished_at_ms INTEGER,
  exec_duration_ms INTEGER,
  created_at INTEGER NOT NULL,
  -- Execution timing belongs to the row's own work: a tool call, or a shell
  -- command the user ran. A run's timing lives on fb_runs.
  CHECK (exec_started_at_ms IS NULL OR role IN ('tool', 'user')),
  CHECK ((exec_started_at_ms IS NULL) = (exec_finished_at_ms IS NULL)
     AND (exec_finished_at_ms IS NULL) = (exec_duration_ms IS NULL))
) STRICT;
CREATE INDEX idx_fb_messages_session_visibility ON fb_messages(session_id, visibility);
CREATE INDEX idx_fb_messages_run ON fb_messages(run_id) WHERE run_id IS NOT NULL;

-- Surface-specific browsing state is deliberately separate from transcript
-- and model context. Rebuilding history never mutates it, and clearing model
-- context never erases where the user was reading.
CREATE TABLE fb_session_ui_state (
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  surface TEXT NOT NULL,
  state_json TEXT NOT NULL DEFAULT '{}',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (session_id, surface)
) STRICT, WITHOUT ROWID;

-- One session's own effective primary-model choice. It is restored whenever
-- that session becomes active; updates are last-write-wins. Identity and
-- reasoning effort only: credentials, base URLs and other provider params
-- always refresh from the shared config file.
CREATE TABLE fb_session_model_state (
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  effort TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (session_id)
) STRICT, WITHOUT ROWID;

-- Prompt state frozen for the life of a session. Content injected ahead of the
-- conversation (the skills catalog, the memory instruction) must stay
-- byte-identical for every request of a session or the provider's cached prefix
-- is re-billed from the first token, so the first render is kept here and
-- replayed. It lives with the session rather than in process memory for two
-- reasons: a resumed session gets the same bytes back after a restart, and the
-- value is released when the session it belongs to is, instead of accumulating
-- for the life of a long-running gateway.
CREATE TABLE fb_session_prompt_state (
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  key TEXT NOT NULL,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (session_id, key)
) STRICT, WITHOUT ROWID;

-- An approval action belongs to one conversation: session_id is the tenant
-- boundary the list API filters by, and the wait that blocks on the action
-- carries the run-level identity.
CREATE TABLE fb_actions (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending', 'answered', 'approved', 'denied', 'cancelled', 'expired', 'error')),
  payload_json TEXT NOT NULL,
  answer_json TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_fb_actions_session_status ON fb_actions(session_id, status, updated_at DESC);
CREATE INDEX idx_fb_actions_pending_created ON fb_actions(created_at) WHERE status = 'pending';

CREATE TABLE fb_runs (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  parent_run_id TEXT REFERENCES fb_runs(id) ON DELETE CASCADE,
  input_text TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('running', 'waiting_action', 'done', 'failed', 'cancelled')),
  usage_prompt_tokens INTEGER NOT NULL DEFAULT 0,
  usage_completion_tokens INTEGER NOT NULL DEFAULT 0,
  usage_cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  usage_cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
  usage_llm_calls INTEGER NOT NULL DEFAULT 0,
  started_at_ms INTEGER,
  finished_at_ms INTEGER,
  worked_ms INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  -- The surface that ran the turn owns its clock and records all three when
  -- the run completes; a run that never completed has none.
  CHECK ((started_at_ms IS NULL) = (finished_at_ms IS NULL)
     AND (finished_at_ms IS NULL) = (worked_ms IS NULL))
) STRICT;
CREATE INDEX idx_fb_runs_session ON fb_runs(session_id, updated_at DESC);
CREATE INDEX idx_fb_runs_parent ON fb_runs(parent_run_id) WHERE parent_run_id IS NOT NULL;
CREATE INDEX idx_fb_runs_active ON fb_runs(status, updated_at DESC) WHERE status IN ('running', 'waiting_action');

-- The single, ordered record of everything a conversation and its runs did.
-- sequence is the conversation cursor surfaces page by; run_id scopes a row to
-- the run that produced it, which is how run-level readers (goal state, the
-- run events API, tool audit) read the same log. Unlike the old run-step
-- ledger it orders parent, child and background-agent events without relying
-- on wall-clock ties.
CREATE TABLE fb_session_events (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  run_id TEXT REFERENCES fb_runs(id) ON DELETE CASCADE,
  event_id TEXT NOT NULL,
  event_type TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}',
  occurred_at_ms INTEGER NOT NULL,
  UNIQUE (session_id, event_id)
) STRICT;
CREATE INDEX idx_fb_session_events_session ON fb_session_events(session_id);
CREATE INDEX idx_fb_session_events_session_type ON fb_session_events(session_id, event_type);
CREATE INDEX idx_fb_session_events_run_type ON fb_session_events(run_id, event_type) WHERE run_id IS NOT NULL;

-- One wait per run: the run is parked at exactly one approval. The resume
-- lease fields are typed columns so each fencing transition is one
-- conditional UPDATE instead of a read-modify-write over a JSON blob.
CREATE TABLE fb_run_waits (
  run_id TEXT PRIMARY KEY REFERENCES fb_runs(id) ON DELETE CASCADE,
  action_id TEXT NOT NULL UNIQUE REFERENCES fb_actions(id) ON DELETE CASCADE,
  tool_name TEXT NOT NULL,
  tool_input_json TEXT NOT NULL,
  session_snapshot_json TEXT NOT NULL DEFAULT '',
  agent_id TEXT NOT NULL DEFAULT '',
  subagent_type TEXT NOT NULL DEFAULT '',
  subagent_run_id TEXT REFERENCES fb_runs(id) ON DELETE SET NULL,
  sandbox_profile TEXT NOT NULL DEFAULT '',
  requested_profile TEXT NOT NULL DEFAULT '',
  profile_elevation INTEGER NOT NULL DEFAULT 0 CHECK (profile_elevation IN (0, 1)),
  resume_owner TEXT NOT NULL DEFAULT '',
  resume_claimed_at_ms INTEGER,
  resume_phase TEXT NOT NULL DEFAULT '' CHECK (resume_phase IN ('', 'claimed', 'execution_started', 'outcome_uncertain')),
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_fb_run_waits_subagent_run ON fb_run_waits(subagent_run_id) WHERE subagent_run_id IS NOT NULL;

CREATE TABLE fb_files (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  original_name TEXT NOT NULL,
  media_type TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  storage_backend TEXT NOT NULL CHECK (storage_backend IN ('local', 's3')),
  storage_bucket TEXT NOT NULL DEFAULT '',
  storage_key TEXT NOT NULL,
  parse_status TEXT NOT NULL CHECK (parse_status IN ('pending', 'running', 'done', 'failed')),
  parsed_text_path TEXT NOT NULL DEFAULT '',
  parse_error TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_fb_files_session ON fb_files(session_id);

-- project_key is the project scope (memories.ProjectKey) the thread this row
-- was extracted from was checked out in. It is required, not backfilled: a
-- thread with no resolvable project (a blank cwd — the gateway/channel case
-- with no launch directory) is never claimed for stage-1 extraction in the
-- first place, so every row here always has a scope to belong to. This is what
-- keeps phase-2 consolidation for one project from ever reading another
-- project's raw memory.
CREATE TABLE fb_memory_stage1_outputs (
  thread_id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  project_key TEXT NOT NULL CHECK (TRIM(project_key) <> ''),
  source_updated_at INTEGER NOT NULL,
  raw_memory TEXT NOT NULL,
  rollout_summary TEXT NOT NULL,
  rollout_slug TEXT,
  generated_at INTEGER NOT NULL,
  usage_count INTEGER NOT NULL DEFAULT 0,
  last_usage INTEGER,
  selected_for_phase2 INTEGER NOT NULL DEFAULT 0 CHECK (selected_for_phase2 IN (0, 1)),
  selected_for_phase2_source_updated_at INTEGER,
  -- The thread's tenant is the session's; the composite key makes the database
  -- hold them equal instead of trusting every writer to.
  FOREIGN KEY (thread_id, agent_id) REFERENCES fb_sessions(id, agent_id) ON DELETE CASCADE
) STRICT;

CREATE INDEX idx_fb_memory_stage1_agent_project
  ON fb_memory_stage1_outputs(agent_id, project_key, source_updated_at DESC, thread_id DESC);

CREATE TABLE fb_memory_jobs (
  kind TEXT NOT NULL,
  job_key TEXT NOT NULL,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  status TEXT NOT NULL,
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
) STRICT, WITHOUT ROWID;

CREATE INDEX idx_fb_memory_jobs_agent_claim
  ON fb_memory_jobs(agent_id, kind, status, retry_at, lease_until);

-- Cron jobs are tenant data: each belongs to one primary agent, and only that
-- agent's jobs run while it is the active one. next_run_at is the scheduler's
-- only index — a NULL means paused, which is how a paused job stays out of the
-- due query without a second flag being consulted.
CREATE TABLE fb_cron_jobs (
  id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  project_id TEXT REFERENCES fb_projects(id) ON DELETE SET NULL,
  name TEXT NOT NULL DEFAULT '',
  schedule TEXT NOT NULL,
  prompt TEXT NOT NULL DEFAULT '',
  deliver TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  repeat_limit INTEGER NOT NULL DEFAULT 0,
  run_count INTEGER NOT NULL DEFAULT 0,
  next_run_at INTEGER,
  last_run_at INTEGER,
  last_status TEXT NOT NULL DEFAULT '' CHECK (last_status IN ('', 'ok', 'failed', 'running', 'delivery_failed')),
  last_error TEXT NOT NULL DEFAULT '',
  last_output TEXT NOT NULL DEFAULT '',
  failure_streak INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_fb_cron_jobs_agent ON fb_cron_jobs(agent_id, created_at DESC);
CREATE INDEX idx_fb_cron_jobs_agent_project ON fb_cron_jobs(agent_id, project_id, created_at DESC);
-- enabled records the user's intent; next_run_at IS NULL means the job is
-- paused or spent, and it is the only thing the due query looks at.
CREATE INDEX idx_fb_cron_jobs_due ON fb_cron_jobs(agent_id, next_run_at) WHERE next_run_at IS NOT NULL;

-- Append-only fire history. It deliberately outlives the job and the session
-- it ran in, so job_id and session_id carry no foreign key.
CREATE TABLE fb_cron_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id TEXT NOT NULL,
  agent_id TEXT NOT NULL CHECK (TRIM(agent_id) <> ''),
  session_id TEXT NOT NULL,
  trigger TEXT NOT NULL CHECK (trigger IN ('schedule', 'manual')),
  status TEXT NOT NULL CHECK (status IN ('running', 'ok', 'failed', 'delivery_failed')),
  output TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  delivered_to TEXT NOT NULL DEFAULT '',
  started_at INTEGER NOT NULL,
  finished_at INTEGER
) STRICT;
CREATE INDEX idx_fb_cron_runs_job ON fb_cron_runs(job_id, started_at DESC);

-- A heartbeat belongs to one session, not to the fleet: it is a recurring
-- instruction inside that conversation, so the session id is the key.
-- next_run_at NULL means the heartbeat is paused.
CREATE TABLE fb_heartbeats (
  session_id TEXT PRIMARY KEY REFERENCES fb_sessions(id) ON DELETE CASCADE,
  interval_seconds INTEGER NOT NULL CHECK (interval_seconds > 0),
  prompt TEXT NOT NULL,
  next_run_at INTEGER,
  last_fired_at INTEGER,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_fb_heartbeats_due ON fb_heartbeats(next_run_at) WHERE next_run_at IS NOT NULL;

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
-- first_rowid/line_count locate the file's rows in fb_memory_fts, so a stale
-- file is dropped by rowid range instead of a scan of the whole index.
CREATE TABLE fb_memory_index_files (
  root TEXT NOT NULL,
  path TEXT NOT NULL,
  size INTEGER NOT NULL,
  mtime_unix INTEGER NOT NULL,
  terms_revision INTEGER NOT NULL DEFAULT 0,
  first_rowid INTEGER NOT NULL,
  line_count INTEGER NOT NULL,
  PRIMARY KEY (root, path)
) STRICT, WITHOUT ROWID;

-- terms is the only indexed column: it holds the line's text after Chinese word
-- segmentation, so FTS5's own tokenizer only has to split on the spaces the
-- segmenter left. Everything else is addressing, carried UNINDEXED so a match
-- can be tied back to the file and line it came from, and scored with bm25().
CREATE VIRTUAL TABLE fb_memory_fts USING fts5(
  terms,
  root UNINDEXED,
  path UNINDEXED,
  line_no UNINDEXED,
  tokenize = 'unicode61 remove_diacritics 2'
);
