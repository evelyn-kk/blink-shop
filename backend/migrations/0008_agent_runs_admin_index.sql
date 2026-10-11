-- 0008 管理端运行列表按状态 / 账号筛选并按创建时间倒序翻页。
ALTER TABLE agent_runs
  ADD KEY idx_agent_runs_status_created (status, created_at),
  ADD KEY idx_agent_runs_account_created (account_id, created_at);
