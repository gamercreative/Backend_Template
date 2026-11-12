SELECT id, conversation_id, run_id, role, content, created_at
FROM (
    SELECT id, conversation_id, run_id, role, content, created_at, seq
    FROM messages
    WHERE conversation_id = $1
    ORDER BY seq DESC
    LIMIT $2
) AS recent
ORDER BY seq ASC
