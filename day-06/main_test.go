package main

import (
	"errors"
	"testing"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
)

func TestRecoveredReplyErrorRequiresAUsableReply(t *testing.T) {
	providerFailure := errors.New("provider 503")
	for _, tc := range []struct {
		name  string
		reply agent.Reply
		err   error
		want  bool
	}{
		{
			name:  "successful answer with an unsaved turn",
			reply: agent.Reply{Text: "ответ"},
			err:   agent.ErrNotSaved,
			want:  true,
		},
		{
			name:  "successful answer with unsummarized old history",
			reply: agent.Reply{Text: "ответ"},
			err:   agent.ErrNotCompressed,
			want:  true,
		},
		{
			name: "failed answer joined to a pre-compression warning",
			err:  errors.Join(providerFailure, agent.ErrNotCompressed),
			want: false,
		},
		{
			name: "empty provider output joined to a pre-compression warning",
			err:  errors.Join(agent.ErrEmptyAnswer, agent.ErrNotCompressed),
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := recoveredReply(tc.reply, tc.err); got != tc.want {
				t.Fatalf("recoveredReply(%+v, %v) = %v, want %v", tc.reply, tc.err, got, tc.want)
			}
		})
	}
}
