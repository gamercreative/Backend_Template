INSERT INTO conversations (agent_id, title)
VALUES ($1, $2)
RETURNING id, agent_id, title, created_at, updated_at
