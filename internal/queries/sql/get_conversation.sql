SELECT id, agent_id, title, created_at, updated_at
FROM conversations
WHERE id = $1
