-- 0007 Prompt 发布记录补充：写入顺序 seq、动作（publish 发布草稿 / rollback 回滚到旧版本或内置）、切换前的 active 版本、原因、推送说明。
-- version = 0 表示内置默认 Prompt（回滚到内置时 prompt_id 为空）；result 为 pending（刚切换，推送 Nacos 中）/ success / fallback（Nacos 推送失败，
-- 运行时以数据库为准）。
ALTER TABLE agent_prompt_publish_records
  ADD COLUMN action       VARCHAR(16)  NOT NULL DEFAULT 'publish' AFTER version,
  ADD COLUMN from_version INT          NOT NULL DEFAULT 0 AFTER action,
  ADD COLUMN reason       VARCHAR(256) NOT NULL DEFAULT '' AFTER published_by,
  ADD COLUMN detail       VARCHAR(256) NOT NULL DEFAULT '' AFTER result,
  ADD COLUMN seq          BIGINT       NOT NULL AUTO_INCREMENT COMMENT '写入顺序：同一毫秒的发布 / 回滚也能按先后排序',
  ADD UNIQUE KEY uk_prompt_publish_seq (seq);
