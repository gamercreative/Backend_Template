INSERT INTO agents (name, system_prompt, model)
VALUES ($1, $2, $3)
RETURNING id, name, system_prompt, model, created_at, updated_at
