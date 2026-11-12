INSERT INTO tool_calls (run_id, name, input, output, is_error, duration_ms)
VALUES ($1, $2, $3::jsonb, $4, $5, $6)
RETURNING id, run_id, name, input, output, is_error, duration_ms, created_at
