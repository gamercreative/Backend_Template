package services

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"alphahack/internal/queries"
)

// MaxModelLen bounds the model identifier stored on an agent.
const MaxModelLen = 100

type agentService struct {
	store AgentStore
}

// NewAgentService returns the AgentService backed by store.
func NewAgentService(store AgentStore) AgentService {
	return &agentService{store: store}
}

// agentValidateName trims and checks the required, bounded agent name.
func agentValidateName(name string) (string, error) {
	name = clean(name)
	if name == "" {
		return "", Invalid("name is required")
	}
	if utf8.RuneCountInString(name) > MaxNameLen {
		return "", Invalid("name must be at most %d characters", MaxNameLen)
	}
	return name, nil
}

// agentValidatePrompt checks the bounded system prompt. It is stored as given.
func agentValidatePrompt(prompt string) error {
	if utf8.RuneCountInString(prompt) > MaxPromptLen {
		return Invalid("system_prompt must be at most %d characters", MaxPromptLen)
	}
	return nil
}

// agentValidateModel trims the model identifier and checks its shape.
// An empty model means the server default.
func agentValidateModel(model string) (string, error) {
	model = clean(model)
	if utf8.RuneCountInString(model) > MaxModelLen {
		return "", Invalid("model must be at most %d characters", MaxModelLen)
	}
	if strings.IndexFunc(model, unicode.IsSpace) >= 0 {
		return "", Invalid("model must not contain whitespace")
	}
	return model, nil
}

func (s *agentService) Create(ctx context.Context, in CreateAgentInput) (queries.Agent, error) {
	name, err := agentValidateName(in.Name)
	if err != nil {
		return queries.Agent{}, err
	}
	if err := agentValidatePrompt(in.SystemPrompt); err != nil {
		return queries.Agent{}, err
	}
	model, err := agentValidateModel(in.Model)
	if err != nil {
		return queries.Agent{}, err
	}
	a, err := s.store.CreateAgent(ctx, queries.CreateAgentParams{
		Name:         name,
		SystemPrompt: in.SystemPrompt,
		Model:        model,
	})
	if err != nil {
		return queries.Agent{}, FromStore(err, "agent")
	}
	return a, nil
}

func (s *agentService) Get(ctx context.Context, id string) (queries.Agent, error) {
	if id == "" {
		return queries.Agent{}, NotFound("agent")
	}
	a, err := s.store.GetAgent(ctx, id)
	if err != nil {
		return queries.Agent{}, FromStore(err, "agent")
	}
	return a, nil
}

func (s *agentService) List(ctx context.Context, p Page) ([]queries.Agent, error) {
	p = p.Normalize()
	list, err := s.store.ListAgents(ctx, p.Limit, p.Offset)
	if err != nil {
		return nil, FromStore(err, "agent")
	}
	return list, nil
}

func (s *agentService) Update(ctx context.Context, id string, in UpdateAgentInput) (queries.Agent, error) {
	if id == "" {
		return queries.Agent{}, NotFound("agent")
	}
	if in.Name == nil && in.SystemPrompt == nil && in.Model == nil {
		return queries.Agent{}, Invalid("at least one field must be provided")
	}
	cur, err := s.store.GetAgent(ctx, id)
	if err != nil {
		return queries.Agent{}, FromStore(err, "agent")
	}
	if in.Name != nil {
		name, err := agentValidateName(*in.Name)
		if err != nil {
			return queries.Agent{}, err
		}
		cur.Name = name
	}
	if in.SystemPrompt != nil {
		if err := agentValidatePrompt(*in.SystemPrompt); err != nil {
			return queries.Agent{}, err
		}
		cur.SystemPrompt = *in.SystemPrompt
	}
	if in.Model != nil {
		model, err := agentValidateModel(*in.Model)
		if err != nil {
			return queries.Agent{}, err
		}
		cur.Model = model
	}
	a, err := s.store.UpdateAgent(ctx, queries.UpdateAgentParams{
		ID:           id,
		Name:         cur.Name,
		SystemPrompt: cur.SystemPrompt,
		Model:        cur.Model,
	})
	if err != nil {
		return queries.Agent{}, FromStore(err, "agent")
	}
	return a, nil
}

func (s *agentService) Delete(ctx context.Context, id string) error {
	if id == "" {
		return NotFound("agent")
	}
	return FromStore(s.store.DeleteAgent(ctx, id), "agent")
}
