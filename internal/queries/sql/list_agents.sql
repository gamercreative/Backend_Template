SELECT id, name, system_prompt, model, created_at, updated_at
FROM agents
ORDER BY created_at DESC, id
LIMIT $1 OFFSET $2
