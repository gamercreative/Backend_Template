UPDATE runs
SET status = $2, stop_reason = $3, error = $4, model = $5,
    input_tokens = $6, output_tokens = $7, iterations = $8,
    finished_at = now()
WHERE id = $1
RETURNING id, conversation_id, status, stop_reason, error, model, input_tokens, output_tokens, iterations, started_at, finished_at
