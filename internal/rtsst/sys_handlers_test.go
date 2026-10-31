// Copyright 2025-2026 Peter Braam
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
