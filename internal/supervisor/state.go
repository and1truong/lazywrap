package supervisor

type State int

const (
	StateStopped State = iota
	StateBuilding
	StateStarting
	StateRunning
	StateStopping
	StateFailed
)
