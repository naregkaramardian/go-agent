package core_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nareg/goagent/core"
	"github.com/nareg/goagent/llm"
)

func TestConversationBuffer_addAndMessages(t *testing.T) {
	buf := core.NewConversationBuffer(10000)
	buf.Add(context.Background(), llm.Message{Role: llm.RoleUser, Content: "hello"})
	buf.Add(context.Background(), llm.Message{Role: llm.RoleAssistant, Content: "world"})

	msgs := buf.Messages()
	require.Len(t, msgs, 2)
	assert.Equal(t, llm.RoleUser, msgs[0].Role)
	assert.Equal(t, "hello", msgs[0].Content)
}

func TestConversationBuffer_tokenCount(t *testing.T) {
	buf := core.NewConversationBuffer(10000)
	assert.Equal(t, 0, buf.TokenCount())

	buf.Add(context.Background(), llm.Message{Role: llm.RoleUser, Content: "hi"})
	assert.Greater(t, buf.TokenCount(), 0)
}

func TestConversationBuffer_trimToFit_noEvictionNeeded(t *testing.T) {
	buf := core.NewConversationBuffer(10000)
	buf.Add(context.Background(), llm.Message{Role: llm.RoleUser, Content: "short"})

	err := buf.TrimToFit(context.Background())
	require.NoError(t, err)
	assert.Len(t, buf.Messages(), 1)
}

func TestConversationBuffer_trimToFit_evictsOldest(t *testing.T) {
	// Budget of 20 tokens; each short message ~ 4 tokens
	buf := core.NewConversationBuffer(20)

	for i := 0; i < 10; i++ {
		buf.Add(context.Background(), llm.Message{
			Role:    llm.RoleUser,
			Content: "msg",
		})
	}

	before := len(buf.Messages())
	err := buf.TrimToFit(context.Background())
	require.NoError(t, err)
	after := len(buf.Messages())

	assert.Less(t, after, before, "should have evicted some messages")
	assert.LessOrEqual(t, buf.TokenCount(), 20, "token count should be within budget")
}

func TestConversationBuffer_trimToFit_keepsLastTwo(t *testing.T) {
	// Very tight budget — can't even hold many messages
	buf := core.NewConversationBuffer(1) // impossible to satisfy, but last 2 must survive

	buf.Add(context.Background(), llm.Message{Role: llm.RoleUser, Content: "first"})
	buf.Add(context.Background(), llm.Message{Role: llm.RoleUser, Content: "second"})
	buf.Add(context.Background(), llm.Message{Role: llm.RoleUser, Content: "third"})

	err := buf.TrimToFit(context.Background())
	require.NoError(t, err)
	msgs := buf.Messages()
	// At minimum the last 2 must remain
	require.GreaterOrEqual(t, len(msgs), 2)
	last := msgs[len(msgs)-1]
	assert.Equal(t, "third", last.Content)
}

func TestConversationBuffer_messagesIsSnapshot(t *testing.T) {
	buf := core.NewConversationBuffer(10000)
	buf.Add(context.Background(), llm.Message{Role: llm.RoleUser, Content: "a"})

	snap := buf.Messages()
	// Mutating the snapshot should not affect the buffer
	snap[0].Content = "mutated"

	fresh := buf.Messages()
	assert.Equal(t, "a", fresh[0].Content)
}
