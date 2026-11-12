SELECT id, agent_id, title, created_at, updated_at
FROM conversations
WHERE ($1::text = '' OR agent_id = $1)
ORDER BY updated_at DESC, id
LIMIT $2 OFFSET $3
