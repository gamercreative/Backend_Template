package queries

import "context"

var (
	qCreateRun = mustSQL("create_run")
	qGetRun    = mustSQL("get_run")
	qListRuns  = mustSQL("list_runs")
	qFinishRun = mustSQL("finish_run")
)

func scanRun(r rowScanner) (Run, error) {
	var run Run
	err := r.Scan(
		&run.ID, &run.ConversationID, &run.Status, &run.StopReason, &run.Error, &run.Model,
		&run.InputTokens, &run.OutputTokens, &run.Iterations, &run.StartedAt, &run.FinishedAt,
	)
	return run, err
}

func (q *Queries) CreateRun(ctx context.Context, p CreateRunParams) (Run, error) {
	run, err := scanRun(q.db.QueryRow(ctx, qCreateRun, p.ConversationID, p.Model))
	return run, wrap("create run", err)
}

func (q *Queries) FinishRun(ctx context.Context, p FinishRunParams) (Run, error) {
	run, err := scanRun(q.db.QueryRow(ctx, qFinishRun,
		p.ID, p.Status, p.StopReason, p.Error, p.Model,
		p.InputTokens, p.OutputTokens, p.Iterations,
	))
	return run, wrap("finish run", err)
}

func (q *Queries) GetRun(ctx context.Context, id string) (Run, error) {
	run, err := scanRun(q.db.QueryRow(ctx, qGetRun, id))
	return run, wrap("get run", err)
}

func (q *Queries) ListRuns(ctx context.Context, conversationID string, limit, offset int) ([]Run, error) {
	rows, err := q.db.Query(ctx, qListRuns, conversationID, limit, offset)
	if err != nil {
		return nil, wrap("list runs", err)
	}
	return collect(rows, "list runs", scanRun)
}
