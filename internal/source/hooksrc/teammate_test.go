package hooksrc

import (
	"testing"

	"github.com/simoncoombes/agentpane/internal/event"
)

func TestMapTeammateIdle(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantID  string
		wantNil bool
	}{
		{
			name:    "named teammate",
			payload: `{"session_id":"s1","hook_event_name":"TeammateIdle","teammate_name":"reviewer","agent_type":"teammate"}`,
			wantID:  "teammate:reviewer",
		},
		{
			name:    "agent_name fallback",
			payload: `{"session_id":"s1","hook_event_name":"TeammateIdle","agent_name":"reviewer"}`,
			wantID:  "teammate:reviewer",
		},
		{
			name:    "nameless dropped, not invented",
			payload: `{"session_id":"s1","hook_event_name":"TeammateIdle","agent_type":"teammate"}`,
			wantNil: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs, err := NewMapper().Map([]byte(tt.payload))
			if err != nil {
				t.Fatalf("Map: %v", err)
			}
			if tt.wantNil {
				if len(evs) != 0 {
					t.Fatalf("want no events, got %v", evs)
				}
				return
			}
			if len(evs) != 1 {
				t.Fatalf("want 1 event, got %d", len(evs))
			}
			ev := evs[0]
			if ev.Kind != event.AgentSpawned {
				t.Errorf("Kind = %v, want AgentSpawned", ev.Kind)
			}
			if ev.AgentID != tt.wantID {
				t.Errorf("AgentID = %q, want %q", ev.AgentID, tt.wantID)
			}
			if ev.AgentType != "teammate" {
				t.Errorf("AgentType = %q, want teammate", ev.AgentType)
			}
			if ev.AgentName != "reviewer" {
				t.Errorf("AgentName = %q, want reviewer", ev.AgentName)
			}
		})
	}
}
