INSERT INTO runs (conversation_id, model)
VALUES ($1, $2)
RETURNING id, conversation_id, status, stop_reason, error, model, input_tokens, output_tokens, iterations, started_at, finished_at
