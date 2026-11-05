package delivery

import "slices"

// TaskAction names an operation staff may run on a task now. Clients show
// only these; every operation checks again when called.
type TaskAction string

const (
	DoRecordShipment   TaskAction = "RECORD_SHIPMENT"
	DoMarkReady        TaskAction = "MARK_READY"
	DoRecordEvent      TaskAction = "RECORD_EVENT"
	DoClassifyAttempt  TaskAction = "CLASSIFY_ATTEMPT"
	DoClose            TaskAction = "CLOSE"
	DoReceive          TaskAction = "RECEIVE"
	DoConfirmCancelled TaskAction = "CONFIRM_CANCELLED"
)

// Actions lists what staff may do with the task in its current status. The
// handover (outbound pickup) and the counter collection after a closed
// shipment depend on the booking and are offered there.
func (t *Task) Actions() []TaskAction {
	var out []TaskAction
	switch t.Status {
	case StatusPendingCreation, StatusCreationFailed, StatusScheduled:
		out = append(out, DoRecordShipment)
	case StatusCreated:
		out = append(out, DoMarkReady)
	case StatusPickedUp, StatusInTransit, StatusDeliveryFailed, StatusCourierAssigned, StatusPickupFailed, StatusPickupConfirmed, StatusReturnFailed:
		if t.Direction == Outbound || t.Status != StatusPickupConfirmed {
			out = append(out, DoRecordEvent)
		}
	case StatusCancelRequested:
		out = append(out, DoConfirmCancelled)
	case StatusManualReview:
		if a, ok := t.FinalAttempt(); ok && t.AwaitingFollowUp() && a.Fault != FaultUnknown {
			out = append(out, DoClose)
		}
	}
	if t.Direction == Return && slices.Contains([]Status{StatusPickupConfirmed, StatusInTransit, StatusDamaged, StatusManualReview}, t.Status) && !t.AwaitingFollowUp() {
		out = append(out, DoReceive)
	}
	if t.Status != StatusClosedUnfulfilled && slices.ContainsFunc(t.Attempts, func(a Attempt) bool { return a.Fault == FaultUnknown }) {
		out = append(out, DoClassifyAttempt)
	}
	return out
}
