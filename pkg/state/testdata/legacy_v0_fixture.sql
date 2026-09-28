-- Rows pinned for the v0→v1 migration tests, built on legacy_v0_schema.sql.
-- Later tasks append rows that cover their own per-table transformations; the
-- shape declared by legacy_v0_schema.sql itself never changes.
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('alpha','main',NULL,2000,1000);
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('beta','main','beta title',3000,2500);
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('gamma','other','gamma title',4000,3500);
INSERT INTO fb_messages(session_id, run_id, role, content, created_at, message_id, parts, model, source, tool_step_id, tool_meta_json) VALUES
 ('alpha','','user','alpha question',1010,'m-alpha-1','[]','','transcript','',''),
 ('alpha','','assistant','alpha answer',1020,'m-alpha-2','[]','model-x','transcript','',''),
 ('beta','','user','beta question',2510,'m-beta-1','[]','','transcript','',''),
 ('beta','','assistant','beta tui echo',2520,'m-beta-2','[]','','tui','',''),
 ('gamma','','tool','gamma tool result',3510,'m-gamma-1','[]','','transcript','ts-1','{"tool_name":"shell"}');

-- A residual table an older binary left behind and no schema ever declared
-- again; the migration must remove it while keeping everything around it.
CREATE TABLE fb_jobs(id TEXT);
INSERT INTO fb_jobs(id) VALUES('stale-job');

-- T2 rows: session and message transforms.
-- delta exercises title normalization, sentinel-to-NULL, origin mapping, the
-- row-id pointer conversions, and created_at rederivation; its messages give
-- the rederivation something to find.
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at, parent_session_id, project_id, origin, compact_boundary_id, context_reset_row_id) VALUES('delta','main','',3900,0,'ghost','prj-missing','migrated','1',3);
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at, origin) VALUES('epsilon','main','   ',4100,4050,'');
INSERT INTO fb_messages(session_id, run_id, role, content, created_at, message_id, parts, model, source) VALUES
 ('delta','run-missing','user','delta question',3100,'m-delta-1','[]',NULL,'transcript'),
 ('delta','run-missing','assistant','delta answer',3200,'m-delta-2','[]',NULL,'transcript_repaired'),
 ('delta','run-missing','assistant','delta withdrawn',3300,'m-delta-3','[]',NULL,'transcript_withdrawn'),
 ('delta','','assistant','delta tui row',3400,'m-delta-4','[]',NULL,'tui'),
 ('delta','','banana','delta weird role',3500,'m-delta-5','[]',NULL,'transcript'),
 ('orphan-session','','user','message of a missing session',3600,'m-orphan','[]',NULL,'transcript');
-- State tables: one row that survives per session and one orphan each.
INSERT INTO fb_session_ui_state(session_id, surface, state_json, updated_at) VALUES
 ('alpha','tui','{"scroll":42}',1200),
 ('orphan-ui','tui','{"scroll":1}',1300);
INSERT INTO fb_session_prompt_state(session_id, key, value, updated_at) VALUES
 ('alpha','skills','["exact-bytes"]',1400),
 ('orphan-ps','skills','["gone"]',1500);

-- T3 rows: runs, actions, and waits.
-- rz-orphan belongs to a session that does not exist; its run and wait drop
-- together. rz-top carries two waits, so the one-wait-per-run pick is
-- observable: the long-run switch wins over the newer shell wait.
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('zeta','main','zeta',5000,4500);
INSERT INTO fb_runs(id, session_id, input_text, status, parent_run_id, created_at, updated_at) VALUES
 ('rz-top','zeta','do things','done','',4500,4900),
 ('rz-sub','zeta','child','done','rz-top',4550,4850),
 ('rz-orphan','ghost-session','ghost','running','',4600,4600);
-- a-z1 carries no session in its payload (resolved through its wait's run);
-- a-z3 the same via the sub run; a-lost resolves neither way and drops;
-- a-ghost names a session that does not exist and drops.
INSERT INTO fb_actions(id, kind, status, payload_json, answer_json, error, created_at, updated_at) VALUES
 ('a-z1','tool_approval','approved','{"tool":"shell"}','','',4700,4750),
 ('a-z2','tool_approval','pending','{"session_id":"zeta"}',NULL,NULL,4800,4800),
 ('a-z3','tool_approval','pending','{}',NULL,NULL,4812,4812),
 ('a-lost','tool_approval','pending','{}',NULL,NULL,4810,4810),
 ('a-ghost','tool_approval','pending','{"session_id":"ghost-session"}',NULL,NULL,4820,4820);
INSERT INTO fb_run_waits(run_id, action_id, tool_name, tool_input_json, session_snapshot_json, source_json, created_at, updated_at) VALUES
 ('rz-top','a-z1','long_run_async_switch','{}','','{"agent_id":"main","subagent_type":"explorer","subagent_run_id":"rz-sub","sandbox_profile":"strict","requested_profile":"loose","profile_elevation":true,"resume_owner":"proc-1","resume_claimed_at":1690000000123,"resume_phase":"claimed"}',4700,4700),
 ('rz-top','a-z2','shell','{}','','{}',4800,4800),
 ('rz-sub','a-z3','shell','{}','','',4810,4810),
 ('rz-orphan','a-lost','shell','{}','','',4815,4815);

-- T4 rows: run timing and old run-id backfill.
-- eta's assistant rows carry the run timing (the same triple on every row of
-- the turn); rz-top's rows name it directly, so the timing lands on it.
-- m-eta-3 is a shell command the user ran: the one user row a writer gives
-- tool metadata, whose timing is its own. m-eta-5 is an ordinary user message
-- that a close-out write stamped with the run's window, as real v0 files hold
-- by the hundred; that window is the run's, not the row's. m-eta-6 is a tool
-- call that finished inside a millisecond: a zero duration is still a window.
INSERT INTO fb_sessions(id, agent_id, title, updated_at, created_at) VALUES('eta','main','eta',6000,5500);
INSERT INTO fb_messages(session_id, run_id, role, content, created_at, message_id, parts, source, tool_step_id, tool_meta_json, run_started_at, run_finished_at, worked_duration_ms) VALUES
 ('eta','','assistant','eta timed answer',5510,'m-eta-1','[]','transcript','','','2026-09-27T10:00:00Z','2026-09-27T10:01:30Z',90000),
 ('eta','','tool','eta tool result',5520,'m-eta-2','[]','transcript','ts-eta','','2026-09-27T10:00:10Z','2026-09-27T10:00:15Z',5000),
 ('eta','','assistant','eta closing answer',5525,'m-eta-4','[]','transcript','','','2026-09-27T10:00:00Z','2026-09-27T10:01:30Z',90000),
 ('eta','','user','<user_shell_command>
<command>
echo hi
</command>
</user_shell_command>',5530,'m-eta-3','[]','transcript','','{"tool_name":"shell","status":"completed"}','2026-09-27T10:00:20Z','2026-09-27T10:00:22Z',2000),
 ('zeta','rz-top','assistant','zeta timed answer',4900,'m-zeta-1','[]','transcript','','','2026-09-27T11:00:00Z','2026-09-27T11:02:00Z',120000),
 ('eta','','user','eta plain question',5540,'m-eta-5','[]','transcript','','','2026-09-27T10:00:00Z','2026-09-27T10:01:30Z',90000),
 ('eta','','tool','eta instant tool result',5545,'m-eta-6','[]','transcript','ts-eta-fast','','2026-09-27T10:00:30.123Z','2026-09-27T10:00:30.123Z',0);

-- T5 rows: the conversation event log and the run-step ledger it absorbs.
-- The events are the authoritative cursor order; the steps are merged into it
-- by time, and only the steps no event already stands for survive.
--
-- evt-ghost names a session that does not exist and drops; evt-norun names a
-- run that does not, so its run link becomes NULL.
INSERT INTO fb_session_events(session_id, event_id, run_id, event_type, payload_json, occurred_at_ms) VALUES
 ('zeta','evt-turn-start','rz-top','turn_started','{}',5000000),
 ('zeta','evt-child-delta','rz-sub','assistant_delta','{"text":"child text"}',5200000),
 ('zeta','evt-twin','rz-top','tool_call_completed','{"kind":"tool_call_completed","step_id":"call-twin","tool_name":"shell"}',5250000),
 ('zeta','evt-main-done','rz-top','turn_completed','{"text":"done"}',5600000),
 ('ghost-session','evt-ghost','','turn_error','{"error":"nowhere"}',5700000),
 ('zeta','evt-norun','rz-missing','assistant_delta','{"text":"orphan run"}',5800000);

-- Steps. The main agent's read_file call (seq 1-2) is migrated and reshaped and
-- lands around evt-turn-start and evt-child-delta; seq 3 already has evt-twin,
-- so it is not migrated; the suppressed, permission-request, plan-card,
-- subagent-plan, child-run, lifecycle, goal and turn steps are not migrated at
-- all — each either has a canonical event of its own or no reader.
INSERT INTO fb_run_steps(run_id, seq, event_type, payload_json, created_at) VALUES
 ('rz-top',1,'tool_call_started','{"kind":"tool_call_started","step_id":"call-migrate","tool_name":"read_file","tool_description":"tool read_file","input":{"file_path":"/repo/a.go"}}',4500),
 ('rz-top',2,'tool_call_completed','{"kind":"tool_call_completed","step_id":"call-migrate","tool_name":"read_file","tool_description":"tool read_file","input":{"file_path":"/repo/a.go"},"output":{"content":"body"},"duration":2500000000}',5100),
 ('rz-top',3,'tool_call_completed','{"kind":"tool_call_completed","step_id":"call-twin","tool_name":"shell","output":{"stdout":"ok"}}',5300),
 ('rz-top',4,'tool_call_started','{"kind":"tool_call_started","step_id":"call-hidden","tool_name":"shell","suppress_ui":true}',5400),
 ('rz-top',5,'tool_call_completed','{"kind":"tool_call_completed","step_id":"call-perm","tool_name":"request_permissions","output":{"permissions":{}}}',5410),
 ('rz-top',6,'tool_call_completed','{"kind":"tool_call_completed","step_id":"call-todo","tool_name":"session_todo","output":{"output":"{\"items\":[]}"}}',5420),
 ('rz-top',7,'plan_updated','{"kind":"plan_updated","step_id":"plan-1","tool_name":"session_todo","plan_update":{"title":"Main plan","total":1,"items":[{"id":"T1","content":"do it","status":"in_progress"}]}}',5430),
 ('rz-top',8,'plan_updated','{"kind":"plan_updated","step_id":"plan-2","tool_name":"session_todo","plan_update":{"title":"Sub plan","agent_id":"task-1","total":1,"items":[]}}',5440),
 ('rz-sub',1,'tool_call_completed','{"kind":"tool_call_completed","step_id":"call-child","tool_name":"read_file","output":{}}',5450),
 ('rz-top',9,'subagent_dispatched','{"task_id":"task-1","agent_type":"explore","status":"running"}',5460),
 ('rz-top',10,'goal_started','{"objective":"ship it"}',5470),
 ('rz-top',11,'turn_started','{}',5480);

-- T6 rows: uploaded files. A local row and an s3 row for zeta both survive,
-- their mutually-exclusive location columns flattening into one storage_key;
-- a workspace_file row (a shape no writer ever produced) and a row for a
-- session that does not exist are dropped.
INSERT INTO fb_files(id, session_id, original_name, media_type, size_bytes, sha256, storage_backend, storage_relpath, oss_bucket, oss_key, oss_etag, oss_version_id, oss_endpoint, parse_status, parsed_text_backend, parsed_text_relpath, parsed_text_oss_bucket, parsed_text_oss_key, parsed_text_oss_etag, error, created_at, updated_at, path, content, version, source) VALUES
 ('f-local','zeta','notes.txt','text/plain',12,'aa-local','local','ab/f-local.txt','','','','','','done','local','f-local.txt','','','','',6100,6150,'','','','attachment'),
 ('f-s3','zeta','paper.pdf','application/pdf',2048,'bb-s3','s3','','bucket-1','ab/f-s3.pdf','etag-1','v1','https://oss.example','failed','local','','','','','parse boom',6200,6250,'','','','attachment'),
 ('f-ws','zeta','scratch.md','text/markdown',5,'cc-ws','local','ab/f-ws.md','','','','','','pending','local','','','','','',6300,6300,'docs/scratch.md','body','3','workspace_file'),
 ('f-ghost','ghost-session','gone.txt','text/plain',1,'dd-ghost','local','ab/f-ghost.txt','','','','','','pending','local','','','','','',6400,6400,'','','','attachment');

-- T7 rows: cron jobs, their fire history and per-session heartbeats. A job's
-- project link is kept only when the project exists; a fire's tenant falls back
-- to its job's and is dropped when neither resolves; a heartbeat's pause
-- collapses into a NULL next fire, and a beat of a missing session is dropped.
-- The project row is here so a live link has something to resolve to.
INSERT INTO fb_projects(id, agent_id, name, root, project_key, created_at, updated_at)
 VALUES('prj-live','main','live','/repo/live','live',7000,7000);
INSERT INTO fb_cron_jobs(id, agent_id, project_id, name, schedule, prompt, deliver, enabled, repeat_limit, run_count, next_run_at, last_run_at, last_status, last_error, last_output, failure_streak, created_at, updated_at) VALUES
 ('job-live','main','prj-live','live job','every 1h','tick','telegram',1,0,3,7200,7100,'ok','','ran',0,7000,7100),
 ('job-dangling','main','prj-missing','dangling job','every 1h','tick','',1,0,0,7200,0,'','','',0,7000,7000);
INSERT INTO fb_cron_runs(job_id, agent_id, run_id, session_id, trigger, status, output, error, delivered_to, started_at, finished_at) VALUES
 ('job-live','main','run-x','cron-s1','schedule','ok','out','','telegram',7100,7110),
 ('job-live','','','cron-s2','manual','failed','','boom','',7120,0),
 ('job-gone','','','cron-s3','schedule','running','','','',7130,0);
INSERT INTO fb_heartbeats(session_id, agent_id, interval_seconds, prompt, paused, last_fired_at, next_run_at, created_at, updated_at) VALUES
 ('alpha','main',300,'beat active',0,7300,7400,7200,7300),
 ('beta','main',300,'beat paused',1,7300,7400,7200,7300),
 ('ghost-session','main',300,'beat ghost',0,0,7400,7200,7200);

-- T8 rows: memory stage-one outputs, the job ledger, and the search index the
-- migration clears (it is derived and rebuilt on the next search). A stage-one
-- row whose tenant does not match its session, and one for a thread that does
-- not exist, are dropped; a NULL usage_count becomes 0 and a non-zero phase-2
-- flag collapses to 1.
INSERT INTO fb_memory_stage1_outputs(thread_id, agent_id, project_key, source_updated_at, raw_memory, rollout_summary, rollout_slug, generated_at, usage_count, last_usage, selected_for_phase2, selected_for_phase2_source_updated_at) VALUES
 ('alpha','main','prj-live',8000,'raw alpha','sum alpha','slug-a',8010,NULL,NULL,2,NULL),
 ('beta','main','prj-live',8100,'raw beta','sum beta',NULL,8110,5,8120,0,NULL),
 ('gamma','main','prj-live',8200,'mislabelled tenant','sum','',8210,1,NULL,1,NULL),
 ('ghost','main','prj-live',8300,'orphan thread','sum',NULL,8310,0,NULL,0,NULL);
INSERT INTO fb_memory_jobs(kind, job_key, agent_id, status, worker_id, ownership_token, started_at, finished_at, lease_until, retry_at, retry_remaining, last_error, input_watermark, last_success_watermark) VALUES
 ('stage1','alpha','main','ok','worker-1','tok-1',8400,8410,NULL,NULL,3,NULL,8420,8420),
 ('consolidate','main:prj-live','main','running','worker-2','tok-2',8500,NULL,8600,NULL,2,'boom',NULL,NULL);
INSERT INTO fb_memory_index_files(root, path, size, mtime_unix, terms_revision) VALUES('/root','f.md',10,1234,7);
INSERT INTO fb_memory_fts(terms, root, path, line_no) VALUES('hello term','/root','f.md',1);
