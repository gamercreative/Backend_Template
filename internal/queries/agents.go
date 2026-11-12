package queries

import "context"

var (
	qCreateAgent = mustSQL("create_agent")
	qGetAgent    = mustSQL("get_agent")
	qListAgents  = mustSQL("list_agents")
	qUpdateAgent = mustSQL("update_agent")
	qDeleteAgent = mustSQL("delete_agent")
)

func scanAgent(r rowScanner) (Agent, error) {
	var a Agent
	err := r.Scan(&a.ID, &a.Name, &a.SystemPrompt, &a.Model, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

func (q *Queries) CreateAgent(ctx context.Context, p CreateAgentParams) (Agent, error) {
	a, err := scanAgent(q.db.QueryRow(ctx, qCreateAgent, p.Name, p.SystemPrompt, p.Model))
	return a, wrap("create agent", err)
}

func (q *Queries) GetAgent(ctx context.Context, id string) (Agent, error) {
	a, err := scanAgent(q.db.QueryRow(ctx, qGetAgent, id))
	return a, wrap("get agent", err)
}

func (q *Queries) ListAgents(ctx context.Context, limit, offset int) ([]Agent, error) {
	rows, err := q.db.Query(ctx, qListAgents, limit, offset)
	if err != nil {
		return nil, wrap("list agents", err)
	}
	return collect(rows, "list agents", scanAgent)
}

func (q *Queries) UpdateAgent(ctx context.Context, p UpdateAgentParams) (Agent, error) {
	a, err := scanAgent(q.db.QueryRow(ctx, qUpdateAgent, p.ID, p.Name, p.SystemPrompt, p.Model))
	return a, wrap("update agent", err)
}

func (q *Queries) DeleteAgent(ctx context.Context, id string) error {
	tag, err := q.db.Exec(ctx, qDeleteAgent, id)
	return affected("delete agent", tag, err)
}
