SELECT id, conversation_id, run_id, role, content, created_at
FROM messages
WHERE conversation_id = $1
ORDER BY seq ASC
LIMIT $2 OFFSET $3
