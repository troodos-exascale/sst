package sst

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// State is a map used to pass data between steps.
type stateInternal map[string]interface{}

// StepFunc defines the signature for any function that can be a step.
type stepFuncInternal func(state stateInternal) error

// Step represents a single, named unit of work in a workflow.
type stepInternal struct {
	Name string
	F    stepFuncInternal
}

// runner is the unexported implementation of the Steps interface.
type runner struct {
	Steps    []*stepInternal
	ProgName string
	state    stateInternal
	stateDir string
}

// newSteps creates and initializes a new step runner.
func newSteps(progName string) Steps {
	return &runner{
		ProgName: progName,
		state:    make(stateInternal),
		stateDir: filepath.Join(os.TempDir(), "steps"),
	}
}

// AddStep adds a new step to the runner's workflow.
func (r *runner) AddStep(name string, f func(state map[string]interface{}) error) {
	// Adapt the function signature to the internal StepFunc type.
	wrappedF := func(s stateInternal) error {
		return f(s)
	}
	r.Steps = append(r.Steps, &stepInternal{Name: name, F: wrappedF})
}

// SetInitialState copies values from a provided map into the runner's internal state.
func (r *runner) SetInitialState(initialState map[string]interface{}) {
	for k, v := range initialState {
		r.state[k] = v
	}
}

// Run executes the defined steps according to the provided flags.
func (r *runner) Run(startStep, untilStep, fromStep int) error {
	if err := os.MkdirAll(r.stateDir, 0755); err != nil {
		return fmt.Errorf("could not create state directory %s: %w", r.stateDir, err)
	}

	if fromStep > 0 {
		if err := r.loadState(fromStep); err != nil {
			return fmt.Errorf("failed to load state for step %d: %w", fromStep, err)
		}
		startStep = fromStep
	}

	startIndex := startStep - 1
	if startIndex < 0 {
		startIndex = 0
	}

	endIndex := len(r.Steps)
	if untilStep > 0 && untilStep <= len(r.Steps) {
		endIndex = untilStep
	}

	var lastStepRun = -1
	for i := startIndex; i < endIndex; i++ {
		step := r.Steps[i]
		fmt.Fprintf(os.Stderr, "[INFO] ==> Running Step %d: %s\n", i+1, step.Name)
		if err := step.F(r.state); err != nil {
			return fmt.Errorf("step %d '%s' failed: %w", i+1, step.Name, err)
		}
		lastStepRun = i
	}

	nextStepIndex := lastStepRun + 1
	if untilStep > 0 && nextStepIndex < len(r.Steps) {
		if err := r.saveState(nextStepIndex); err != nil {
			return fmt.Errorf("failed to save state for next step: %w", err)
		}
		fmt.Fprintf(os.Stderr, "[INFO] Workflow paused. State saved for step %d.\n", nextStepIndex+1)
	}

	return nil
}

func (r *runner) saveState(nextStepNum int) error {
	stateFile := filepath.Join(r.stateDir, fmt.Sprintf("%s-step-args-%d", r.ProgName, nextStepNum))
	data, err := json.MarshalIndent(r.state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}
	return os.WriteFile(stateFile, data, 0644)
}

func (r *runner) loadState(fromStepNum int) error {
	pattern := filepath.Join(r.stateDir, fmt.Sprintf("%s-step-args-%d*", r.ProgName, fromStepNum))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return fmt.Errorf("no state file found matching pattern: %s", pattern)
	}
	sort.Strings(matches)
	latestFile := matches[len(matches)-1]

	fmt.Fprintf(os.Stderr, "[INFO] Resuming from state file: %s\n", latestFile)
	data, err := os.ReadFile(latestFile)
	if err != nil {
		return fmt.Errorf("failed to read state file %s: %w", latestFile, err)
	}
	if err := json.Unmarshal(data, &r.state); err != nil {
		return fmt.Errorf("failed to unmarshal state from %s: %w", latestFile, err)
	}

	return os.Remove(latestFile)
}
