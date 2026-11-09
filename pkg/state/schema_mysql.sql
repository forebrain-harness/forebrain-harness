-- The state schema in MySQL 8.0: the same tables, columns and index names as
-- schema.sql, in MySQL's types and DDL (plan 209, design §2.4, §2.9).
--
-- schema.sql is the SQLite file and is never edited. This file declares the
-- same version of the same shape; a fresh MySQL database is created from it
-- whole and stamped with the version in fb_schema_version, and later
-- migrations carry both files forward together.
--
-- How the translation goes:
--   INTEGER                     -> BIGINT (every stored time is milliseconds)
--   TEXT holding an id or a key -> VARCHAR(191)
--   TEXT holding prose or JSON  -> TEXT / LONGTEXT (a MySQL VARCHAR that is
--                                  too short fails the write instead of
--                                  truncating it, so only bounded values get a
--                                  width)
--   AUTOINCREMENT               -> AUTO_INCREMENT
--   STRICT / WITHOUT ROWID      -> dropped (InnoDB is neither)
--   a partial index             -> a plain index, or for a partial UNIQUE index
--                                  a unique index on an expression, where an
--                                  empty value becomes NULL and many NULLs do
--                                  not conflict
--   a table whose rows the store orders by SQLite's rowid gets an explicit
--   row_id AUTO_INCREMENT column, which the MySQL statements order by. Their
--   keys are named without the idx_ prefix, the way every index here is: an
--   idx_ name is a name schema.sql also has.
--
-- Keywords MySQL reserves are backticked as columns: `call`, `key`, `trigger`.
-- Every statement ends with ';' and the executor runs them one at a time.
--
-- Foreign keys are declared in one ALTER block at the end of this file rather
-- than beside their columns: MySQL parses a column-level REFERENCES clause and
-- creates nothing, so a key written there would look right and enforce nothing.
-- The block also lets every table exist before any key points at it, which the
-- fb_sessions / fb_runs / fb_messages cycle needs.

CREATE TABLE fb_schema_version (
  version BIGINT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE fb_projects (
  id VARCHAR(191) NOT NULL PRIMARY KEY,
  agent_id VARCHAR(191) NOT NULL CHECK (TRIM(agent_id) <> ''),
  name TEXT NOT NULL,
  icon TEXT NOT NULL DEFAULT (''),
  description LONGTEXT NOT NULL DEFAULT (''),
  instructions LONGTEXT NOT NULL DEFAULT (''),
  root TEXT NOT NULL,
  project_key VARCHAR(191) NOT NULL,
  memory_scope VARCHAR(32) NOT NULL DEFAULT 'shared' CHECK (memory_scope IN ('shared', 'project_only')),
  resource_access BIGINT NOT NULL DEFAULT 1 CHECK (resource_access IN (0, 1)),
  pinned BIGINT NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1)),
  -- The directory the project's "save to" dialog last uploaded into, relative
  -- to the root with '/' separators.
  last_attach_dir TEXT NOT NULL DEFAULT (''),
  -- What kind of project this is (office | coding | batch), frozen into each
  -- conversation the project starts.
  project_type VARCHAR(32) NOT NULL DEFAULT 'coding' CHECK (project_type IN ('office', 'coding', 'batch')),
  -- Whether this batch project's tasks run inside their task boundary.
  batch_sandbox BIGINT NOT NULL DEFAULT 1 CHECK (batch_sandbox IN (0, 1)),
  -- Where a batch project's end-of-execution notifications go: a JSON array of
  -- {"channel","target"} pairs.
  task_notify_json LONGTEXT NOT NULL DEFAULT ('[]'),
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  KEY idx_fb_projects_agent_updated (agent_id, updated_at DESC),
  -- SQLite indexes the whole root; a root has no length limit the store can
  -- promise, so the key is taken over its SHA-256, which is exactly as unique.
  UNIQUE KEY idx_fb_projects_agent_root_unique (agent_id, (SHA2(root, 256)))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- One delivery channel a project owns. A secret field is stored in plaintext
-- (owner 2026-11-07, plan 268); a page only ever sees a mask of it.
CREATE TABLE fb_project_channels (
  project_id VARCHAR(191) NOT NULL,
  kind VARCHAR(64) NOT NULL CHECK (TRIM(kind) <> ''),
  enabled BIGINT NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
  config_json LONGTEXT NOT NULL DEFAULT ('{}'),
  account_key VARCHAR(255) NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (project_id, kind),
  -- WHERE account_key <> '': an empty key becomes NULL here, and a unique
  -- index does not compare NULLs, so any number of channels may have none.
  UNIQUE KEY idx_fb_project_channels_account ((NULLIF(account_key, '')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- The compact/context-reset pointers reference fb_messages rows, and
-- fb_messages references this table back: InnoDB, like SQLite, checks foreign
-- keys at DML time rather than at create time, so the cycle is declarable —
-- but only in the order the tables are created, which is why those two
-- constraints are added after fb_messages.
CREATE TABLE fb_sessions (
  id VARCHAR(191) NOT NULL PRIMARY KEY,
  agent_id VARCHAR(191) NOT NULL CHECK (TRIM(agent_id) <> ''),
  title TEXT NOT NULL CHECK (TRIM(title) <> ''),
  parent_session_id VARCHAR(191),
  project_id VARCHAR(191),
  origin VARCHAR(32) NOT NULL DEFAULT 'native' CHECK (origin IN ('native', 'migrated')),
  -- source marks what a session is FOR ('' = an ordinary conversation,
  -- 'workshop' = a skill-workshop task).
  source TEXT NOT NULL DEFAULT (''),
  -- The project type this conversation was born with, frozen at birth.
  project_type VARCHAR(32) NOT NULL DEFAULT 'coding' CHECK (project_type IN ('office', 'coding', 'batch')),
  cwd TEXT NOT NULL DEFAULT (''),
  git_branch TEXT NOT NULL DEFAULT (''),
  memory_mode TEXT NOT NULL DEFAULT ('disabled'),
  memory_source TEXT NOT NULL DEFAULT (''),
  initial_window_id TEXT NOT NULL DEFAULT (''),
  compact_boundary_message_id BIGINT,
  context_reset_message_id BIGINT,
  created_at BIGINT NOT NULL CHECK (created_at > 0),
  updated_at BIGINT NOT NULL CHECK (updated_at > 0),
  KEY idx_fb_sessions_agent_updated (agent_id, updated_at DESC),
  KEY idx_fb_sessions_agent_project (agent_id, project_id, updated_at DESC),
  KEY idx_fb_sessions_parent (parent_session_id),
  -- SQLite's UNIQUE (id, agent_id), which fb_memory_stage1_outputs references.
  UNIQUE KEY fb_sessions_id_agent_unique (id, agent_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE fb_runs (
  id VARCHAR(191) NOT NULL PRIMARY KEY,
  -- SQLite's rowid: the order runs were inserted in, which the store falls
  -- back on when two timestamps tie.
  row_id BIGINT NOT NULL AUTO_INCREMENT,
  session_id VARCHAR(191) NOT NULL,
  parent_run_id VARCHAR(191),
  input_text LONGTEXT NOT NULL,
  status VARCHAR(32) NOT NULL CHECK (status IN ('running', 'waiting_action', 'done', 'failed', 'cancelled')),
  usage_prompt_tokens BIGINT NOT NULL DEFAULT 0,
  usage_completion_tokens BIGINT NOT NULL DEFAULT 0,
  usage_cache_read_tokens BIGINT NOT NULL DEFAULT 0,
  usage_cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
  usage_llm_calls BIGINT NOT NULL DEFAULT 0,
  -- Wall-clock time spent waiting on model requests during this run.
  usage_llm_ms BIGINT NOT NULL DEFAULT 0,
  started_at_ms BIGINT,
  finished_at_ms BIGINT,
  worked_ms BIGINT,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  -- owner is the process driving the run, meaningful only while it is running.
  owner TEXT NOT NULL DEFAULT (''),
  -- How long the turn waited for the model's first output.
  first_token_ms BIGINT,
  CHECK ((started_at_ms IS NULL) = (finished_at_ms IS NULL)
     AND (finished_at_ms IS NULL) = (worked_ms IS NULL)),
  UNIQUE KEY fb_runs_row_id (row_id),
  KEY idx_fb_runs_session (session_id, updated_at DESC),
  KEY idx_fb_runs_parent (parent_run_id),
  KEY idx_fb_runs_active (status, updated_at DESC),
  KEY idx_fb_runs_session_live (session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- The stored transcript. visibility hides a row from display and from the next
-- model request while keeping it on disk.
CREATE TABLE fb_messages (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  session_id VARCHAR(191) NOT NULL,
  run_id VARCHAR(191),
  role VARCHAR(32) NOT NULL CHECK (role IN ('user', 'assistant', 'tool', 'reasoning', 'system')),
  visibility VARCHAR(32) NOT NULL DEFAULT 'visible' CHECK (visibility IN ('visible', 'repaired', 'withdrawn', 'rewound')),
  content LONGTEXT NOT NULL,
  parts LONGTEXT NOT NULL DEFAULT ('[]'),
  message_id TEXT NOT NULL DEFAULT (''),
  model TEXT NOT NULL DEFAULT (''),
  usage_json LONGTEXT NOT NULL DEFAULT (''),
  tool_step_id TEXT NOT NULL DEFAULT (''),
  tool_meta_json LONGTEXT NOT NULL DEFAULT (''),
  exec_started_at_ms BIGINT,
  exec_finished_at_ms BIGINT,
  exec_duration_ms BIGINT,
  created_at BIGINT NOT NULL,
  -- Execution timing belongs to the row's own work: a tool call, or a shell
  -- command the user ran. A run's timing lives on fb_runs.
  CHECK (exec_started_at_ms IS NULL OR role IN ('tool', 'user')),
  CHECK ((exec_started_at_ms IS NULL) = (exec_finished_at_ms IS NULL)
     AND (exec_finished_at_ms IS NULL) = (exec_duration_ms IS NULL)),
  KEY idx_fb_messages_session_visibility (session_id, visibility),
  KEY idx_fb_messages_run (run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- One row per file a file tool wrote in a conversation.
CREATE TABLE fb_file_changes (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  session_id VARCHAR(191) NOT NULL,
  run_id VARCHAR(191) NOT NULL DEFAULT '',
  step_id VARCHAR(191) NOT NULL DEFAULT '',
  -- The fb_messages row id of the user message this write's turn belongs to.
  anchor_message_id BIGINT NOT NULL DEFAULT 0,
  -- Absolute, normalized path of the file the tool wrote.
  path TEXT NOT NULL,
  -- 1 when the file existed before the write, 0 when the tool created it.
  existed_before BIGINT NOT NULL DEFAULT 0 CHECK (existed_before IN (0, 1)),
  -- Snapshot of the file's bytes before the write; empty when it did not exist.
  before_snapshot LONGTEXT NOT NULL DEFAULT (''),
  -- sha256 of the file's bytes after the write; empty when the write deleted it.
  after_sha256 CHAR(64) NOT NULL DEFAULT '',
  deleted BIGINT NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
  created_at_ms BIGINT NOT NULL,
  KEY idx_fb_file_changes_session_anchor (session_id, anchor_message_id),
  -- An index over a whole path would exceed InnoDB's key length. The prefix
  -- bounds the key; a lookup still compares the stored column in full.
  KEY idx_fb_file_changes_session_path (session_id, path(500))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- One row per "rewind to before this message".
CREATE TABLE fb_rewinds (
  id VARCHAR(191) NOT NULL PRIMARY KEY,
  session_id VARCHAR(191) NOT NULL,
  anchor_message_id BIGINT NOT NULL,
  mode VARCHAR(32) NOT NULL CHECK (mode IN ('both', 'chat', 'code')),
  -- The sequence of the first fb_session_events row the rewind hides; 0 when
  -- no events are hidden (a code-only rewind).
  hidden_event_seq_from BIGINT NOT NULL DEFAULT 0,
  -- JSON: the files the rewind restored, each with the snapshot of the bytes
  -- as they were just before the rewind (for undo) and whether it existed then.
  files_json LONGTEXT NOT NULL DEFAULT ('[]'),
  state VARCHAR(32) NOT NULL CHECK (state IN ('pending', 'fixed')),
  created_at_ms BIGINT NOT NULL,
  KEY idx_fb_rewinds_session_state (session_id, state)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Surface-specific browsing state, deliberately separate from the transcript.
CREATE TABLE fb_session_ui_state (
  session_id VARCHAR(191) NOT NULL,
  surface VARCHAR(191) NOT NULL,
  state_json LONGTEXT NOT NULL DEFAULT ('{}'),
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (session_id, surface)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- One session's own effective primary-model choice.
CREATE TABLE fb_session_model_state (
  session_id VARCHAR(191) NOT NULL,
  provider VARCHAR(191) NOT NULL,
  model VARCHAR(191) NOT NULL,
  effort VARCHAR(64) NOT NULL DEFAULT '',
  model_id VARCHAR(191) NOT NULL DEFAULT '',
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- One session's own approval-preset choice (the read-only / default /
-- full-access pair of approval mode and sandbox mode).
CREATE TABLE fb_session_permission_state (
  session_id VARCHAR(191) NOT NULL,
  preset VARCHAR(64) NOT NULL,
  source VARCHAR(64) NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- One session's observed image rejections, coarsely: a provider that refused
-- an image for this conversation is remembered per provider+model.
CREATE TABLE fb_session_image_refusals (
  session_id VARCHAR(191) NOT NULL,
  provider VARCHAR(191) NOT NULL,
  model VARCHAR(191) NOT NULL,
  reason TEXT NOT NULL DEFAULT (''),
  observed_at BIGINT NOT NULL,
  PRIMARY KEY (session_id, provider, model)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Prompt state frozen for the life of a session: content injected ahead of the
-- conversation must stay byte-identical for every request of a session.
CREATE TABLE fb_session_prompt_state (
  session_id VARCHAR(191) NOT NULL,
  `key` VARCHAR(191) NOT NULL,
  value LONGTEXT NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (session_id, `key`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- An approval action belongs to one conversation.
CREATE TABLE fb_actions (
  id VARCHAR(191) NOT NULL PRIMARY KEY,
  session_id VARCHAR(191) NOT NULL,
  kind VARCHAR(191) NOT NULL,
  status VARCHAR(32) NOT NULL CHECK (status IN ('pending', 'answered', 'approved', 'denied', 'cancelled', 'expired', 'error')),
  payload_json LONGTEXT NOT NULL,
  answer_json LONGTEXT NOT NULL DEFAULT (''),
  error LONGTEXT NOT NULL DEFAULT (''),
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  KEY idx_fb_actions_session_status (session_id, status, updated_at DESC),
  -- WHERE status = 'pending', which an index cannot say on MySQL: the status
  -- is compared and the ordered scan is the index's.
  KEY idx_fb_actions_pending_created (created_at, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- One row per living forebrain process, renewed on a lease interval. The last
-- three columns are the replica registration (plan 213 §3.1).
CREATE TABLE fb_run_owners (
  owner VARCHAR(191) NOT NULL PRIMARY KEY CHECK (TRIM(owner) <> ''),
  heartbeat_at_ms BIGINT NOT NULL,
  internal_addr VARCHAR(1024) NOT NULL DEFAULT (''),
  host VARCHAR(255) NOT NULL DEFAULT (''),
  kind VARCHAR(32) NOT NULL DEFAULT ('')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- One lease per background singleton that only one replica of a shared
-- database may run at a time (plan 213 §3.4).
CREATE TABLE fb_leases (
  name VARCHAR(191) NOT NULL PRIMARY KEY,
  holder VARCHAR(191) NOT NULL,
  expires_at_ms BIGINT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- The single, ordered record of everything a conversation and its runs did.
CREATE TABLE fb_session_events (
  sequence BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  session_id VARCHAR(191) NOT NULL,
  run_id VARCHAR(191),
  event_id VARCHAR(191) NOT NULL,
  event_type VARCHAR(191) NOT NULL,
  payload_json LONGTEXT NOT NULL DEFAULT ('{}'),
  occurred_at_ms BIGINT NOT NULL,
  KEY idx_fb_session_events_session (session_id),
  KEY idx_fb_session_events_session_type (session_id, event_type),
  KEY idx_fb_session_events_run_type (run_id, event_type),
  UNIQUE KEY fb_session_events_session_event_unique (session_id, event_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- One wait per run: the run is parked at exactly one approval.
CREATE TABLE fb_run_waits (
  run_id VARCHAR(191) NOT NULL PRIMARY KEY,
  action_id VARCHAR(191) NOT NULL UNIQUE,
  tool_name VARCHAR(191) NOT NULL,
  tool_input_json LONGTEXT NOT NULL,
  session_snapshot_json LONGTEXT NOT NULL DEFAULT (''),
  agent_id VARCHAR(191) NOT NULL DEFAULT '',
  subagent_type VARCHAR(191) NOT NULL DEFAULT '',
  subagent_run_id VARCHAR(191),
  sandbox_profile VARCHAR(191) NOT NULL DEFAULT '',
  requested_profile VARCHAR(191) NOT NULL DEFAULT '',
  profile_elevation BIGINT NOT NULL DEFAULT 0 CHECK (profile_elevation IN (0, 1)),
  resume_owner VARCHAR(191) NOT NULL DEFAULT '',
  resume_claimed_at_ms BIGINT,
  resume_phase VARCHAR(32) NOT NULL DEFAULT '' CHECK (resume_phase IN ('', 'claimed', 'execution_started', 'outcome_uncertain')),
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  KEY idx_fb_run_waits_subagent_run (subagent_run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- One row per physical model request a run made. NULL token columns mean "not
-- reported", never an estimate.
CREATE TABLE fb_llm_attempts (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  run_id VARCHAR(191) NOT NULL,
  `call` BIGINT NOT NULL,
  attempt BIGINT NOT NULL,
  streaming BIGINT,
  status BIGINT NOT NULL DEFAULT 0,
  error LONGTEXT NOT NULL DEFAULT (''),
  wait_ms BIGINT NOT NULL DEFAULT 0,
  elapsed_ms BIGINT NOT NULL DEFAULT 0,
  input_tokens BIGINT,
  output_tokens BIGINT,
  outcome VARCHAR(32) NOT NULL CHECK (outcome IN ('success', 'failed', 'kept-partial', 'cancelled')),
  created_at_ms BIGINT NOT NULL,
  KEY idx_fb_llm_attempts_run (run_id, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE fb_files (
  id VARCHAR(191) NOT NULL PRIMARY KEY,
  session_id VARCHAR(191) NOT NULL,
  original_name TEXT NOT NULL,
  media_type VARCHAR(191) NOT NULL,
  size_bytes BIGINT NOT NULL,
  sha256 CHAR(64) NOT NULL,
  storage_backend VARCHAR(32) NOT NULL CHECK (storage_backend IN ('local', 's3')),
  storage_bucket TEXT NOT NULL DEFAULT (''),
  storage_key TEXT NOT NULL,
  -- Where the original lives when the upload was saved into the project the
  -- session belongs to.
  project_path TEXT NOT NULL DEFAULT (''),
  parse_status VARCHAR(32) NOT NULL CHECK (parse_status IN ('pending', 'running', 'done', 'failed')),
  parsed_text_path TEXT NOT NULL DEFAULT (''),
  parse_error LONGTEXT NOT NULL DEFAULT (''),
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  KEY idx_fb_files_session (session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- project_key is the project scope the thread this row was extracted from was
-- checked out in. It is required, not backfilled.
CREATE TABLE fb_memory_stage1_outputs (
  thread_id VARCHAR(191) NOT NULL PRIMARY KEY,
  agent_id VARCHAR(191) NOT NULL,
  project_key VARCHAR(191) NOT NULL CHECK (TRIM(project_key) <> ''),
  source_updated_at BIGINT NOT NULL,
  raw_memory LONGTEXT NOT NULL,
  rollout_summary LONGTEXT NOT NULL,
  rollout_slug VARCHAR(191),
  generated_at BIGINT NOT NULL,
  usage_count BIGINT NOT NULL DEFAULT 0,
  last_usage BIGINT,
  selected_for_phase2 BIGINT NOT NULL DEFAULT 0 CHECK (selected_for_phase2 IN (0, 1)),
  selected_for_phase2_source_updated_at BIGINT,
  -- How far into the thread's transcript this extraction read: the id of the
  -- last fb_messages row it consumed.
  last_message_row_id BIGINT,
  KEY idx_fb_memory_stage1_agent_project (agent_id, project_key, source_updated_at DESC, thread_id DESC),
  -- The thread's tenant is the session's; the composite key makes the database
  -- hold them equal instead of trusting every writer to.
  FOREIGN KEY (thread_id, agent_id) REFERENCES fb_sessions(id, agent_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

CREATE TABLE fb_memory_jobs (
  kind VARCHAR(64) NOT NULL,
  job_key VARCHAR(191) NOT NULL,
  agent_id VARCHAR(191) NOT NULL CHECK (TRIM(agent_id) <> ''),
  status VARCHAR(64) NOT NULL,
  ownership_token VARCHAR(191),
  started_at BIGINT,
  finished_at BIGINT,
  lease_until BIGINT,
  retry_at BIGINT,
  retry_remaining BIGINT NOT NULL,
  last_error LONGTEXT,
  input_watermark BIGINT,
  last_success_watermark BIGINT,
  PRIMARY KEY (kind, job_key),
  KEY idx_fb_memory_jobs_agent_claim (agent_id, kind, status, retry_at, lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Cron jobs are tenant data: each belongs to one primary agent and to one of
-- that agent's projects. next_run_at is the scheduler's only index — a NULL
-- means paused.
CREATE TABLE fb_cron_jobs (
  id VARCHAR(191) NOT NULL PRIMARY KEY,
  agent_id VARCHAR(191) NOT NULL CHECK (TRIM(agent_id) <> ''),
  project_id VARCHAR(191) NOT NULL,
  name TEXT NOT NULL DEFAULT (''),
  schedule TEXT NOT NULL,
  prompt LONGTEXT NOT NULL DEFAULT (''),
  deliver LONGTEXT NOT NULL DEFAULT (''),
  enabled BIGINT NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  repeat_limit BIGINT NOT NULL DEFAULT 0,
  run_count BIGINT NOT NULL DEFAULT 0,
  next_run_at BIGINT,
  last_run_at BIGINT,
  last_status VARCHAR(32) NOT NULL DEFAULT '' CHECK (last_status IN ('', 'ok', 'failed', 'running', 'delivery_failed')),
  last_error LONGTEXT NOT NULL DEFAULT (''),
  last_error_code VARCHAR(64) NOT NULL DEFAULT '',
  last_output LONGTEXT NOT NULL DEFAULT (''),
  failure_streak BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  KEY idx_fb_cron_jobs_agent_project (agent_id, project_id, created_at DESC),
  -- WHERE next_run_at IS NOT NULL: a MySQL index holds NULLs, and the due
  -- query's comparison skips them.
  KEY idx_fb_cron_jobs_due (agent_id, next_run_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Append-only fire history. It deliberately outlives the job and the session it
-- ran in, so job_id and session_id carry no foreign key.
CREATE TABLE fb_cron_runs (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  job_id VARCHAR(191) NOT NULL,
  agent_id VARCHAR(191) NOT NULL CHECK (TRIM(agent_id) <> ''),
  session_id VARCHAR(191) NOT NULL,
  `trigger` VARCHAR(32) NOT NULL CHECK (`trigger` IN ('schedule', 'manual')),
  status VARCHAR(32) NOT NULL CHECK (status IN ('running', 'ok', 'failed', 'delivery_failed')),
  output LONGTEXT NOT NULL DEFAULT (''),
  error LONGTEXT NOT NULL DEFAULT (''),
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  delivered_to LONGTEXT NOT NULL DEFAULT (''),
  started_at BIGINT NOT NULL,
  finished_at BIGINT,
  KEY idx_fb_cron_runs_job (job_id, started_at DESC),
  -- A run's end finds its fire by the session it ran in.
  KEY idx_fb_cron_runs_session (session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- A heartbeat is a recurring instruction inside one conversation, so the
-- session id is the key. next_run_at NULL means paused.
CREATE TABLE fb_heartbeats (
  session_id VARCHAR(191) NOT NULL PRIMARY KEY,
  interval_seconds BIGINT NOT NULL CHECK (interval_seconds > 0),
  prompt LONGTEXT NOT NULL,
  next_run_at BIGINT,
  last_fired_at BIGINT,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  KEY idx_fb_heartbeats_due (next_run_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- The memory search index: one row per line of every file under a memory root,
-- carrying that line already segmented into index terms. It is derived data and
-- is rebuilt from the files themselves.
CREATE TABLE fb_memory_index_files (
  root TEXT NOT NULL,
  path TEXT NOT NULL,
  size BIGINT NOT NULL,
  mtime_unix BIGINT NOT NULL,
  terms_revision BIGINT NOT NULL DEFAULT 0,
  first_rowid BIGINT NOT NULL,
  line_count BIGINT NOT NULL,
  -- SQLite's PRIMARY KEY (root, path), which InnoDB cannot hold over two paths;
  -- the SHA-256 of the pair is exactly as unique. CHAR(0) separates the two
  -- parts and cannot appear in a path. The name has no idx_ prefix because
  -- schema.sql has no such index to mirror.
  UNIQUE KEY fb_memory_index_files_root_path_unique ((SHA2(CONCAT(root, CHAR(0), path), 256)))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- terms is the only indexed column: it holds the line's text after Chinese word
-- segmentation, and the ngram parser splits it the way FTS5's own tokenizer
-- splits on the spaces the segmenter left. The rest is addressing, kept out of
-- the index, so a match can be tied back to the file and line it came from.
CREATE TABLE fb_memory_fts (
  -- SQLite's rowid, which fb_memory_index_files.first_rowid addresses.
  row_id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  terms LONGTEXT NOT NULL,
  root TEXT NOT NULL,
  path TEXT NOT NULL,
  line_no BIGINT NOT NULL,
  FULLTEXT KEY fb_memory_fts_terms_fulltext (terms) WITH PARSER ngram
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- The migration ledger: one row per import unit a /migrate run already carried
-- into the state database.
CREATE TABLE fb_migration_units (
  agent_id VARCHAR(191) NOT NULL,
  source VARCHAR(64) NOT NULL,
  target_id VARCHAR(191) NOT NULL,
  source_path TEXT NOT NULL,
  source_bytes BIGINT NOT NULL,
  source_mtime_ms BIGINT NOT NULL,
  message_count BIGINT NOT NULL DEFAULT 0,
  plans_json LONGTEXT,
  imported_at_ms BIGINT NOT NULL,
  PRIMARY KEY (agent_id, target_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- A file a conversation delivered. run_id is a plain reference (no foreign
-- key): an artifact must outlive the pruning of run rows.
CREATE TABLE fb_artifacts (
  id VARCHAR(191) NOT NULL PRIMARY KEY,
  -- SQLite's rowid, which the store orders same-timestamp artifacts by.
  row_id BIGINT NOT NULL AUTO_INCREMENT,
  agent_id VARCHAR(191) NOT NULL CHECK (TRIM(agent_id) <> ''),
  session_id VARCHAR(191) NOT NULL,
  run_id VARCHAR(191) NOT NULL DEFAULT '',
  tool_call_id VARCHAR(191) NOT NULL DEFAULT '',
  path TEXT NOT NULL,
  rel_path TEXT NOT NULL DEFAULT (''),
  name TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT (''),
  description LONGTEXT NOT NULL DEFAULT (''),
  mime VARCHAR(191) NOT NULL,
  kind VARCHAR(191) NOT NULL,
  preview LONGTEXT NOT NULL DEFAULT (''),
  size BIGINT NOT NULL,
  mtime_ns BIGINT NOT NULL DEFAULT 0,
  sha256 CHAR(64) NOT NULL DEFAULT '',
  origin VARCHAR(32) NOT NULL DEFAULT 'model' CHECK (origin IN ('model', 'fallback', 'plan')),
  created_at BIGINT NOT NULL,
  UNIQUE KEY fb_artifacts_row_id (row_id),
  KEY idx_fb_artifacts_session (session_id, created_at),
  KEY idx_fb_artifacts_run (run_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- A batch project's business task. A task is run by one or more executions.
CREATE TABLE fb_tasks (
  id VARCHAR(191) NOT NULL PRIMARY KEY,
  agent_id VARCHAR(191) NOT NULL CHECK (TRIM(agent_id) <> ''),
  project_id VARCHAR(191) NOT NULL,
  title TEXT NOT NULL DEFAULT (''),
  input_text LONGTEXT NOT NULL DEFAULT (''),
  input_set BIGINT NOT NULL DEFAULT 0 CHECK (input_set IN (0, 1)),
  external_ref TEXT NOT NULL DEFAULT (''),
  origin VARCHAR(32) NOT NULL DEFAULT 'api' CHECK (origin IN ('api', 'web', 'cron', 'channel')),
  origin_ref VARCHAR(191) NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  KEY idx_fb_tasks_project_updated (project_id, updated_at DESC),
  -- WHERE external_ref <> '': every row is indexed and the comparison filters.
  KEY idx_fb_tasks_external_ref (project_id, external_ref(500))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- One execution of a task: exactly one conversation and one task workspace.
CREATE TABLE fb_task_executions (
  session_id VARCHAR(191) NOT NULL PRIMARY KEY,
  task_id VARCHAR(191) NOT NULL,
  seq BIGINT NOT NULL CHECK (seq > 0),
  workspace TEXT NOT NULL,
  status VARCHAR(32) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'stopped', 'completed', 'failed')),
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  error_text LONGTEXT NOT NULL DEFAULT (''),
  -- Why the end-of-execution notification could not be delivered.
  notify_error LONGTEXT NOT NULL DEFAULT (''),
  started_at BIGINT,
  finished_at BIGINT,
  created_at BIGINT NOT NULL,
  UNIQUE KEY fb_task_executions_task_seq_unique (task_id, seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- The install's runtime configuration, one row per root section of
-- forebrain.yaml (plan 187).
CREATE TABLE fb_config (
  section VARCHAR(191) NOT NULL PRIMARY KEY,
  value_json LONGTEXT NOT NULL,
  updated_at_ms BIGINT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- The install's model configurations: one row per (provider, model, base_url,
-- api_path), shared by every primary agent (owner 2026-11-07). The same model
-- may be configured several times — behind different endpoints, paths or keys —
-- so a model is referred to by this row's id, never by "provider/model".
-- api_key is stored in plaintext (owner ruling) and never leaves the gateway.
-- The cap_* columns override the model catalog for this row; NULL (or '')
-- means "use the catalog".
CREATE TABLE fb_llm_models (
  id VARCHAR(191) NOT NULL PRIMARY KEY,
  provider VARCHAR(191) NOT NULL CHECK (TRIM(provider) <> ''),
  model VARCHAR(191) NOT NULL CHECK (TRIM(model) <> ''),
  base_url TEXT NOT NULL DEFAULT (''),
  api_path VARCHAR(191) NOT NULL DEFAULT '',
  api_key TEXT NOT NULL DEFAULT (''),
  params_json LONGTEXT NOT NULL DEFAULT (''),
  cap_images BIGINT CHECK (cap_images IN (0, 1)),
  cap_pdf BIGINT CHECK (cap_pdf IN (0, 1)),
  cap_video BIGINT CHECK (cap_video IN (0, 1)),
  cap_audio BIGINT CHECK (cap_audio IN (0, 1)),
  cap_reasoning BIGINT CHECK (cap_reasoning IN (0, 1)),
  cap_reasoning_off BIGINT CHECK (cap_reasoning_off IN (0, 1)),
  cap_efforts_json TEXT NOT NULL DEFAULT (''),
  cap_default_effort VARCHAR(32) NOT NULL DEFAULT '',
  cap_context_window BIGINT,
  cap_max_output BIGINT,
  source VARCHAR(64) NOT NULL DEFAULT '',
  source_plan VARCHAR(64) NOT NULL DEFAULT '',
  url_verbatim BIGINT NOT NULL DEFAULT 0 CHECK (url_verbatim IN (0, 1)),
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  -- base_url has no length the store can promise: keyed over its SHA-256,
  -- exactly as unique (the fb_projects root precedent).
  UNIQUE KEY idx_fb_llm_models_identity (provider, model, (SHA2(base_url, 256)), api_path),
  KEY idx_fb_llm_models_created (created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Operator-owned variables that ${NAME} references in MCP and LSP server
-- entries resolve from (owner 2026-11-07, replacing ~/.forebrain/.env). A
-- project-scoped entry resolves only from here — never from the process
-- environment — so a repository cannot read the host's secrets.
CREATE TABLE fb_env_vars (
  name VARCHAR(191) NOT NULL PRIMARY KEY,
  value LONGTEXT NOT NULL,
  updated_at_ms BIGINT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- MCP OAuth refresh tokens, one row per (namespace, server). An empty
-- project_key is the shared global store.
CREATE TABLE fb_mcp_oauth (
  project_key VARCHAR(191) NOT NULL,
  server_name VARCHAR(191) NOT NULL,
  value_json LONGTEXT NOT NULL,
  updated_at_ms BIGINT NOT NULL,
  PRIMARY KEY (project_key, server_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- The read baseline a conversation keeps across processes (plan 183): for a
-- path read_file put in front of the model, the version it showed.
CREATE TABLE fb_file_read_states (
  session_id VARCHAR(191) NOT NULL,
  agent_id VARCHAR(191) NOT NULL CHECK (TRIM(agent_id) <> ''),
  path TEXT NOT NULL,
  mod_time_ns BIGINT NOT NULL,
  size BIGINT NOT NULL,
  sha256 CHAR(64) NOT NULL,
  read_at_ns BIGINT NOT NULL,
  touch_kind VARCHAR(32) NOT NULL DEFAULT 'read' CHECK (touch_kind = 'read'),
  -- SQLite's PRIMARY KEY (session_id, path), which InnoDB cannot hold over a
  -- whole path: the digest of the path is exactly as unique.
  path_sha CHAR(64) GENERATED ALWAYS AS (SHA2(path, 256)) STORED NOT NULL,
  PRIMARY KEY (session_id, path_sha)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- The tool-output history: the original text behind every output the
-- compression layer shortened, keyed by the retrieval id the marker cites. It
-- lives in the state database in every mode — one install keeps exactly one
-- database (design §2.6).
CREATE TABLE output_records (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  kind VARCHAR(16) NOT NULL CHECK (kind IN ('shell', 'mcp')),
  command LONGTEXT NOT NULL,
  session_id VARCHAR(191) NOT NULL DEFAULT '',
  original_output LONGTEXT NOT NULL,
  filtered_output LONGTEXT NOT NULL,
  original_output_bytes BIGINT NOT NULL,
  filtered_output_bytes BIGINT NOT NULL,
  saved_tokens BIGINT NOT NULL,
  capability_id VARCHAR(191) NOT NULL DEFAULT '',
  capability_version VARCHAR(64) NOT NULL DEFAULT '',
  retrieve_count BIGINT NOT NULL DEFAULT 0,
  retrieve_reason LONGTEXT NOT NULL DEFAULT (''),
  spool_path LONGTEXT NOT NULL DEFAULT (''),
  spool_omitted_bytes BIGINT NOT NULL DEFAULT 0,
  created_at_ms BIGINT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Every foreign key: the same references schema.sql declares, each named after
-- the column that carries it. The first two close the fb_sessions /
-- fb_messages cycle — each table points at the other, and both exist by now.
ALTER TABLE fb_sessions ADD CONSTRAINT fk_fb_sessions_compact_boundary_message_id FOREIGN KEY (compact_boundary_message_id) REFERENCES fb_messages(id) ON DELETE SET NULL;
ALTER TABLE fb_sessions ADD CONSTRAINT fk_fb_sessions_context_reset_message_id FOREIGN KEY (context_reset_message_id) REFERENCES fb_messages(id) ON DELETE SET NULL;
ALTER TABLE fb_project_channels ADD CONSTRAINT fk_fb_project_channels_project_id FOREIGN KEY (project_id) REFERENCES fb_projects(id) ON DELETE CASCADE;
ALTER TABLE fb_sessions ADD CONSTRAINT fk_fb_sessions_parent_session_id FOREIGN KEY (parent_session_id) REFERENCES fb_sessions(id) ON DELETE SET NULL;
ALTER TABLE fb_sessions ADD CONSTRAINT fk_fb_sessions_project_id FOREIGN KEY (project_id) REFERENCES fb_projects(id) ON DELETE SET NULL;
ALTER TABLE fb_runs ADD CONSTRAINT fk_fb_runs_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_runs ADD CONSTRAINT fk_fb_runs_parent_run_id FOREIGN KEY (parent_run_id) REFERENCES fb_runs(id) ON DELETE CASCADE;
ALTER TABLE fb_messages ADD CONSTRAINT fk_fb_messages_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_messages ADD CONSTRAINT fk_fb_messages_run_id FOREIGN KEY (run_id) REFERENCES fb_runs(id) ON DELETE SET NULL;
ALTER TABLE fb_file_changes ADD CONSTRAINT fk_fb_file_changes_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_rewinds ADD CONSTRAINT fk_fb_rewinds_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_session_ui_state ADD CONSTRAINT fk_fb_session_ui_state_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_session_model_state ADD CONSTRAINT fk_fb_session_model_state_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_session_permission_state ADD CONSTRAINT fk_fb_session_permission_state_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_session_image_refusals ADD CONSTRAINT fk_fb_session_image_refusals_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_session_prompt_state ADD CONSTRAINT fk_fb_session_prompt_state_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_actions ADD CONSTRAINT fk_fb_actions_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_session_events ADD CONSTRAINT fk_fb_session_events_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_session_events ADD CONSTRAINT fk_fb_session_events_run_id FOREIGN KEY (run_id) REFERENCES fb_runs(id) ON DELETE CASCADE;
ALTER TABLE fb_run_waits ADD CONSTRAINT fk_fb_run_waits_run_id FOREIGN KEY (run_id) REFERENCES fb_runs(id) ON DELETE CASCADE;
ALTER TABLE fb_run_waits ADD CONSTRAINT fk_fb_run_waits_action_id FOREIGN KEY (action_id) REFERENCES fb_actions(id) ON DELETE CASCADE;
ALTER TABLE fb_run_waits ADD CONSTRAINT fk_fb_run_waits_subagent_run_id FOREIGN KEY (subagent_run_id) REFERENCES fb_runs(id) ON DELETE SET NULL;
ALTER TABLE fb_llm_attempts ADD CONSTRAINT fk_fb_llm_attempts_run_id FOREIGN KEY (run_id) REFERENCES fb_runs(id) ON DELETE CASCADE;
ALTER TABLE fb_files ADD CONSTRAINT fk_fb_files_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_heartbeats ADD CONSTRAINT fk_fb_heartbeats_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_artifacts ADD CONSTRAINT fk_fb_artifacts_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_tasks ADD CONSTRAINT fk_fb_tasks_project_id FOREIGN KEY (project_id) REFERENCES fb_projects(id) ON DELETE CASCADE;
ALTER TABLE fb_task_executions ADD CONSTRAINT fk_fb_task_executions_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
ALTER TABLE fb_task_executions ADD CONSTRAINT fk_fb_task_executions_task_id FOREIGN KEY (task_id) REFERENCES fb_tasks(id) ON DELETE CASCADE;
ALTER TABLE fb_file_read_states ADD CONSTRAINT fk_fb_file_read_states_session_id FOREIGN KEY (session_id) REFERENCES fb_sessions(id) ON DELETE CASCADE;
