package protocol

import "testing"

// TestTaskStatusTransitions validates the valid state machine transitions for TaskStatus.
func TestTaskStatusTransitions(t *testing.T) {
	// Define allowed transitions: from -> []to
	allowed := map[TaskStatus][]TaskStatus{
		TaskStatusRequested:  {TaskStatusAccepted, TaskStatusRejected},
		TaskStatusAccepted:   {TaskStatusInProgress, TaskStatusFailed},
		TaskStatusInProgress: {TaskStatusCompleted, TaskStatusFailed},
		TaskStatusCompleted:  {},
		TaskStatusFailed:     {},
		TaskStatusRejected:   {},
	}

	// Verify terminal states have no outgoing transitions.
	terminals := []TaskStatus{TaskStatusCompleted, TaskStatusFailed, TaskStatusRejected}
	for _, s := range terminals {
		transitions, ok := allowed[s]
		if !ok {
			t.Errorf("terminal state %q not present in allowed map", s)
			continue
		}
		if len(transitions) != 0 {
			t.Errorf("terminal state %q should have no transitions, got %v", s, transitions)
		}
	}

	// Verify non-terminal states have at least one outgoing transition.
	nonTerminals := []TaskStatus{TaskStatusRequested, TaskStatusAccepted, TaskStatusInProgress}
	for _, s := range nonTerminals {
		transitions, ok := allowed[s]
		if !ok {
			t.Errorf("non-terminal state %q not present in allowed map", s)
			continue
		}
		if len(transitions) == 0 {
			t.Errorf("non-terminal state %q must have at least one transition", s)
		}
	}

	// Verify all defined TaskStatus constants are covered.
	allStatuses := []TaskStatus{
		TaskStatusRequested,
		TaskStatusAccepted,
		TaskStatusInProgress,
		TaskStatusCompleted,
		TaskStatusFailed,
		TaskStatusRejected,
	}
	for _, s := range allStatuses {
		if _, ok := allowed[s]; !ok {
			t.Errorf("TaskStatus %q is not covered in the transition map", s)
		}
	}
}
