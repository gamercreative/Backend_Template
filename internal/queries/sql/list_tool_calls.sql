SELECT id, run_id, name, input, output, is_error, duration_ms, created_at
FROM tool_calls
WHERE run_id = $1
ORDER BY seq ASC
