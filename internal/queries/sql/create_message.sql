INSERT INTO messages (conversation_id, run_id, role, content)
VALUES ($1, $2, $3, $4)
RETURNING id, conversation_id, run_id, role, content, created_at
