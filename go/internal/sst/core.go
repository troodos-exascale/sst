package sst

// core is the unexported, concrete implementation of the Core interface.
type core struct {
	logx    Logx
	files   Files
	project Project
}

// New initializes and returns the Core interface, containing all functionalities.
func New() (Core, error) {
	f, err := newFiles()
	if err != nil {
		return nil, err
	}
	return &core{
		logx:    newLogx(),
		files:   f,
		project: newProject(),
	}, nil
}

func (c *core) Logx() Logx {
	return c.logx
}

func (c *core) Files() Files {
	return c.files
}

func (c *core) Steps(progName string) Steps {
	return newSteps(progName)
}

func (c *core) Project() Project {
	return c.project
}
