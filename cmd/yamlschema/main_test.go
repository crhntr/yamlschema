package main

import (
	"bytes"
	"os"
	"strconv"
	"testing"

	"rsc.io/script"
	"rsc.io/script/scripttest"
)

// Test runs the scripts in testdata. Each is a txtar archive: commands
// first, then the files they use.
func Test(t *testing.T) {
	e := script.NewEngine()
	e.Cmds = script.DefaultCmds()
	e.Conds = script.DefaultConds()
	e.Cmds["yamlschema"] = script.Command(script.CmdUsage{
		Summary: "yamlschema",
	}, func(state *script.State, args ...string) (script.WaitFunc, error) {
		return func(state *script.State) (string, string, error) {
			var stdout, stderr bytes.Buffer
			code := run(state.Context(), state.Getwd(), args, &stdout, &stderr)
			if code != 0 {
				return stdout.String(), stderr.String(), exitCode(code)
			}
			return stdout.String(), stderr.String(), nil
		}, nil
	})
	env := []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
	scripttest.Test(t, t.Context(), e, env, "testdata/*.txt")
}

type exitCode int

func (c exitCode) Error() string { return "exit " + strconv.Itoa(int(c)) }
