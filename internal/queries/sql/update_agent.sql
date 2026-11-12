UPDATE agents
SET name = $2, system_prompt = $3, model = $4, updated_at = now()
WHERE id = $1
RETURNING id, name, system_prompt, model, created_at, updated_at
