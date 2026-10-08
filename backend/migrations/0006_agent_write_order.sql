-- 0006 导购消息和运行轨迹按写入顺序读取：同一毫秒内的多条也要能排出先后（与 0005 审计表的做法一致）。
ALTER TABLE user_messages
  ADD COLUMN seq BIGINT NOT NULL AUTO_INCREMENT COMMENT '写入顺序' AFTER message_id,
  ADD UNIQUE KEY uk_user_messages_seq (seq);

ALTER TABLE agent_trace_events
  ADD COLUMN seq BIGINT NOT NULL AUTO_INCREMENT COMMENT '写入顺序' AFTER trace_event_id,
  ADD UNIQUE KEY uk_agent_trace_events_seq (seq);
