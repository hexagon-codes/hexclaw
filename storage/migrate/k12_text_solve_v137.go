package migrate

// K12TextSolveV137 为文字求解提供输入、实际执行和不可变结算，不创建批改任务。
var K12TextSolveV137 = Migration{Version: 137, Description: "K12 durable text solve execution and finalization", SQL: `
CREATE TABLE IF NOT EXISTS k12_text_solve_runs (
 owner_id TEXT NOT NULL, agent_name TEXT NOT NULL, solve_run_id TEXT NOT NULL,
 input_digest TEXT NOT NULL, facts_digest TEXT NOT NULL, data TEXT NOT NULL CHECK(json_valid(data)),
 created_at INTEGER NOT NULL, PRIMARY KEY(owner_id,agent_name,solve_run_id),
 FOREIGN KEY(agent_name) REFERENCES agents(name) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS k12_text_solve_invocations (
 invocation_id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, agent_name TEXT NOT NULL, solve_run_id TEXT NOT NULL,
 operation TEXT NOT NULL, execution_kind TEXT NOT NULL, operation_attempt INTEGER NOT NULL,
 request_digest TEXT NOT NULL, status TEXT NOT NULL, data TEXT NOT NULL CHECK(json_valid(data)),
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
 UNIQUE(owner_id,agent_name,solve_run_id,operation,operation_attempt),
 UNIQUE(owner_id,agent_name,solve_run_id,operation,execution_kind,request_digest),
 FOREIGN KEY(owner_id,agent_name,solve_run_id) REFERENCES k12_text_solve_runs(owner_id,agent_name,solve_run_id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS k12_text_solve_finalizations (
 finalization_id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, agent_name TEXT NOT NULL, solve_run_id TEXT NOT NULL,
 result_digest TEXT NOT NULL, data TEXT NOT NULL CHECK(json_valid(data)), created_at INTEGER NOT NULL,
 UNIQUE(owner_id,agent_name,solve_run_id),
 FOREIGN KEY(owner_id,agent_name,solve_run_id) REFERENCES k12_text_solve_runs(owner_id,agent_name,solve_run_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_k12_text_solve_invocation_run ON k12_text_solve_invocations(owner_id,agent_name,solve_run_id,status);
CREATE TRIGGER IF NOT EXISTS k12_text_solve_run_immutable BEFORE UPDATE ON k12_text_solve_runs
 BEGIN SELECT RAISE(ABORT,'text solve input is immutable'); END;
CREATE TRIGGER IF NOT EXISTS k12_text_solve_finalization_immutable BEFORE UPDATE ON k12_text_solve_finalizations
 BEGIN SELECT RAISE(ABORT,'text solve finalization is immutable'); END;
`}
