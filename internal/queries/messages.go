package queries

import "context"

var (
	qCreateMessage      = mustSQL("create_message")
	qListMessages       = mustSQL("list_messages")
	qListRecentMessages = mustSQL("list_recent_messages")
)

func scanMessage(r rowScanner) (Message, error) {
	var m Message
	err := r.Scan(&m.ID, &m.ConversationID, &m.RunID, &m.Role, &m.Content, &m.CreatedAt)
	return m, err
}

func (q *Queries) CreateMessage(ctx context.Context, p CreateMessageParams) (Message, error) {
	m, err := scanMessage(q.db.QueryRow(ctx, qCreateMessage, p.ConversationID, p.RunID, p.Role, p.Content))
	return m, wrap("create message", err)
}

func (q *Queries) ListMessages(ctx context.Context, conversationID string, limit, offset int) ([]Message, error) {
	rows, err := q.db.Query(ctx, qListMessages, conversationID, limit, offset)
	if err != nil {
		return nil, wrap("list messages", err)
	}
	return collect(rows, "list messages", scanMessage)
}

func (q *Queries) ListRecentMessages(ctx context.Context, conversationID string, limit int) ([]Message, error) {
	rows, err := q.db.Query(ctx, qListRecentMessages, conversationID, limit)
	if err != nil {
		return nil, wrap("list recent messages", err)
	}
	return collect(rows, "list recent messages", scanMessage)
}
