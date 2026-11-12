SELECT id, conversation_id, status, stop_reason, error, model, input_tokens, output_tokens, iterations, started_at, finished_at
FROM runs
WHERE id = $1
