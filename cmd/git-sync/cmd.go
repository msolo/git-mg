package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path"
	"strings"
	"syscall"

	log "github.com/msolo/go-bis/glug"
	"github.com/pkg/errors"
)

type Cmd struct {
	*exec.Cmd
	trace bool
}

var trace bool

func init() {
	trace = true
}

func (cmd *Cmd) bashString() string {
	args := make([]string, len(cmd.Args))
	for i, x := range cmd.Args {
		args[i] = bashQuote(x)
	}
	return strings.Join(args, " ")
}

type ExitError struct {
	*exec.ExitError
	*exec.Cmd
}

func (xe *ExitError) Cause() error {
	return xe.ExitError
}

func (xe *ExitError) Error() string {
	return fmt.Sprintf("cmd failed: %s\n%s", xe.ExitError, xe.ExitError.Stderr)
}

func Command(name string, arg ...string) *Cmd {
	cmd := exec.Command(name, arg...)
	return &Cmd{Cmd: cmd, trace: trace}
}

func CommandContext(ctx context.Context, name string, arg ...string) *Cmd {
	cmd := exec.CommandContext(ctx, name, arg...)
	return &Cmd{Cmd: cmd, trace: trace}
}

func wrapErr(err error, cmd *exec.Cmd) error {
	err = errors.Cause(err)
	if exitErr, ok := err.(*exec.ExitError); ok {
		prefix := "  " + path.Base(cmd.Args[0]) + ": "
		if len(exitErr.Stderr) > 0 {
			exitErr.Stderr = append([]byte(prefix),
				bytes.Replace(exitErr.Stderr[:len(exitErr.Stderr)-1], []byte("\n"), []byte("\n"+prefix), -1)...)
			exitErr.Stderr = append(exitErr.Stderr, '\n')
		}
		return &ExitError{exitErr, cmd}
	}
	return err
}

// We may want stderr to leak through since otherwise you get *no*
// information.  Run() doesn't capture any stderr. Most likely you
// just want to use Output() and toss the data.
func (cmd *Cmd) Run() error {
	if cmd.trace {
		defer log.Tracef("perf: {{.durationStr}} exec: %s", cmd.bashString()).Finish()
	}
	return wrapErr(cmd.Cmd.Run(), cmd.Cmd)
}

func (cmd *Cmd) Wait() error {
	return wrapErr(cmd.Cmd.Wait(), cmd.Cmd)
}

func (cmd *Cmd) Output() ([]byte, error) {
	if cmd.trace {
		defer log.Tracef("perf: {{.durationStr}} exec: %s", cmd.bashString()).Finish()
	}
	data, err := cmd.Cmd.Output()
	err = wrapErr(err, cmd.Cmd)
	return data, err
}

func (cmd *Cmd) CombinedOutput() ([]byte, error) {
	if cmd.trace {
		defer log.Tracef("perf {{.durationStr}} exec: %s", cmd.bashString()).Finish()
	}
	data, err := cmd.Cmd.CombinedOutput()
	err = wrapErr(err, cmd.Cmd)
	return data, err
}

func exitStatus(err error) (int, error) {
	err = errors.Cause(err)
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.Sys().(syscall.WaitStatus).ExitStatus(), nil
	}
	return 0, errors.New("invalid error type")
}
