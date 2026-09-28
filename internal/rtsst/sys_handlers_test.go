package rtsst

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSysHandler_ArgumentExpansion(t *testing.T) {
	inst := Instruction{
		Type: "RUN",
		Args: `sys.test_cmd --dir="${MY_DIR}" --flag="true"`,
		Env: map[string]string{
			"MY_DIR": "/tmp/test",
		},
	}

	expandedCmd := os.Expand(inst.Args, func(k string) string { return inst.Env[k] })
	parts := strings.Fields(expandedCmd)
	
	args := parts[1:]
	parsed := parseSysArgsArray(args)

	assert.Equal(t, "/tmp/test", parsed["dir"], "Environment variables should be expanded and quotes stripped")
	assert.Equal(t, "true", parsed["flag"], "Quotes should be stripped")
}
