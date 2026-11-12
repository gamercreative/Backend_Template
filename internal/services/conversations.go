package services

import (
	"context"
	"unicode/utf8"

	"alphahack/internal/queries"
)

type conversationService struct {
	store ConversationStore
}

// NewConversationService returns the conversation rules on top of store.
func NewConversationService(store ConversationStore) ConversationService {
	return &conversationService{store: store}
}

func (s *conversationService) Create(ctx context.Context, in CreateConversationInput) (queries.Conversation, error) {
	agentID := clean(in.AgentID)
	if agentID == "" {
		return queries.Conversation{}, Invalid("agent_id is required")
	}
	title := clean(in.Title)
	if utf8.RuneCountInString(title) > MaxNameLen {
		return queries.Conversation{}, Invalid("title must be at most %d characters", MaxNameLen)
	}
	if _, err := s.store.GetAgent(ctx, agentID); err != nil {
		return queries.Conversation{}, FromStore(err, "agent")
	}
	c, err := s.store.CreateConversation(ctx, queries.CreateConversationParams{AgentID: agentID, Title: title})
	if err != nil {
		return queries.Conversation{}, FromStore(err, "conversation")
	}
	return c, nil
}

func (s *conversationService) Get(ctx context.Context, id string) (queries.Conversation, error) {
	if id == "" {
		return queries.Conversation{}, NotFound("conversation")
	}
	c, err := s.store.GetConversation(ctx, id)
	if err != nil {
		return queries.Conversation{}, FromStore(err, "conversation")
	}
	return c, nil
}

func (s *conversationService) List(ctx context.Context, agentID string, p Page) ([]queries.Conversation, error) {
	p = p.Normalize()
	cs, err := s.store.ListConversations(ctx, clean(agentID), p.Limit, p.Offset)
	if err != nil {
		return nil, FromStore(err, "conversation")
	}
	return cs, nil
}

func (s *conversationService) Delete(ctx context.Context, id string) error {
	if id == "" {
		return NotFound("conversation")
	}
	return FromStore(s.store.DeleteConversation(ctx, id), "conversation")
}

func (s *conversationService) ListMessages(ctx context.Context, conversationID string, p Page) ([]queries.Message, error) {
	if conversationID == "" {
		return nil, NotFound("conversation")
	}
	if _, err := s.store.GetConversation(ctx, conversationID); err != nil {
		return nil, FromStore(err, "conversation")
	}
	p = p.Normalize()
	ms, err := s.store.ListMessages(ctx, conversationID, p.Limit, p.Offset)
	if err != nil {
		return nil, FromStore(err, "message")
	}
	return ms, nil
}
