SELECT id, conversation_id, status, stop_reason, error, model, input_tokens, output_tokens, iterations, started_at, finished_at
FROM runs
WHERE conversation_id = $1
ORDER BY started_at DESC, id
LIMIT $2 OFFSET $3
