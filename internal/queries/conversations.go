package queries

import "context"

var (
	qCreateConversation = mustSQL("create_conversation")
	qGetConversation    = mustSQL("get_conversation")
	qListConversations  = mustSQL("list_conversations")
	qTouchConversation  = mustSQL("touch_conversation")
	qDeleteConversation = mustSQL("delete_conversation")
)

func scanConversation(r rowScanner) (Conversation, error) {
	var c Conversation
	err := r.Scan(&c.ID, &c.AgentID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func (q *Queries) CreateConversation(ctx context.Context, p CreateConversationParams) (Conversation, error) {
	c, err := scanConversation(q.db.QueryRow(ctx, qCreateConversation, p.AgentID, p.Title))
	return c, wrap("create conversation", err)
}

func (q *Queries) GetConversation(ctx context.Context, id string) (Conversation, error) {
	c, err := scanConversation(q.db.QueryRow(ctx, qGetConversation, id))
	return c, wrap("get conversation", err)
}

func (q *Queries) ListConversations(ctx context.Context, agentID string, limit, offset int) ([]Conversation, error) {
	rows, err := q.db.Query(ctx, qListConversations, agentID, limit, offset)
	if err != nil {
		return nil, wrap("list conversations", err)
	}
	return collect(rows, "list conversations", scanConversation)
}

func (q *Queries) TouchConversation(ctx context.Context, id string) error {
	tag, err := q.db.Exec(ctx, qTouchConversation, id)
	return affected("touch conversation", tag, err)
}

func (q *Queries) DeleteConversation(ctx context.Context, id string) error {
	tag, err := q.db.Exec(ctx, qDeleteConversation, id)
	return affected("delete conversation", tag, err)
}
