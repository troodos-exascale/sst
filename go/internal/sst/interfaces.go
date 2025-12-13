package sst

import "context"

// Logx is the interface for the structured, traceable logger.
type Logx interface {
	With(kv ...any) Logx
	WithError(err error) Logx
	Debug(ctx context.Context, msg string, kv ...any)
	Info(ctx context.Context, msg string, kv ...any)
	Warn(ctx context.Context, msg string, kv ...any)
	Error(ctx context.Context, msg string, kv ...any)
	Subsystem(name string) Logx
}

// Files provides a security-fenced interface for file operations.
type Files interface {
	Fence(p string) (string, error)
	Root() string
}

// Steps provides an interface for running multi-step, stateful workflows.
type Steps interface {
	AddStep(name string, f func(state map[string]interface{}) error)
	Run(startStep, untilStep, fromStep int) error
	SetInitialState(initialState map[string]interface{})
}

// Project provides an interface for scaffolding and activating SST projects.
type Project interface {
	Activate(path, containerPath string, force bool) error
}

// Core is the master interface that provides access to all sub-package functionalities.
type Core interface {
	Logx() Logx
	Files() Files
	Steps(progName string) Steps
	Project() Project
}
