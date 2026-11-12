SELECT id, name, system_prompt, model, created_at, updated_at
FROM agents
WHERE id = $1
