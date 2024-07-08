package types

type ListenerConfig struct {
	Table              string   // Can be prefixed by schema - e.g: public.posts
	Fields             []string // Empty fields means all ( SELECT * )
	MaxParallelProcess int      // Default to 1 (not parallel) -> use -1 for Infinity
}

type Operation uint8

const (
	OperationInsert Operation = 1 << iota
	OperationUpdate
	OperationDelete
	OperationTruncate

	OperationAll = OperationInsert | OperationUpdate | OperationDelete | OperationTruncate
)

type EventData map[string]any

type EventCallback func(event Event)

type Event interface {
	GetOperation() Operation
}

type InsertEvent struct {
	New *EventData
}
type UpdateEvent struct {
	Old *EventData
	New *EventData
}
type DeleteEvent struct {
	Old *EventData
}
type TruncateEvent struct{}

func (e *InsertEvent) GetOperation() Operation {
	return OperationInsert
}

func (e *UpdateEvent) GetOperation() Operation {
	return OperationUpdate
}

func (e *DeleteEvent) GetOperation() Operation {
	return OperationDelete
}

func (e *TruncateEvent) GetOperation() Operation {
	return OperationTruncate
}
