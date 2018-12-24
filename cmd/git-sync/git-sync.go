package main

import (
	"context"
	"fmt"

	"os"
	"os/exec"
	"time"

	log "github.com/amoghe/distillog"

	"github.com/msolo/cmdflag"
)

var cmdPush = &cmdflag.Command{
	Name:      "push",
	Run:       runPush,
	UsageLine: "push <remote name>",
	UsageLong: `Push a working directory to a remote working dir.`,
}

type traceSpan struct {
	name    string
	start   time.Time
	elapsed time.Duration
}

func (ts *traceSpan) Finish() {
	ts.elapsed = time.Now().Sub(ts.start)
}

func newTrace(name string) *traceSpan {
	return &traceSpan{name: name, start: time.Now()}
}

func handleCmdError(err error) {
	if exitErr, ok := err.(*exec.ExitError); ok {
		fmt.Fprintln(os.Stderr, "  cmd stderr:", string(exitErr.Stderr))
	}
}

func exitOnError(err error) {
	if err != nil {
		handleCmdError(err)
		log.Errorf("%s\n", err)
		panic(ExitCode(1))
	}
}

func runPush(ctx context.Context, cmd *cmdflag.Command, args []string) {
	cfg, err := readConfigFromGit()
	exitOnError(err)

	gitWorkdir := getGitWorkdir()
	_, err = fullSync(cfg, gitWorkdir)
	exitOnError(err)
}

var cmdMain = &cmdflag.Command{
	Name:      "git-sync",
	UsageLong: `git-sync - a tool to sync working directories`,
	Flags: []cmdflag.Flag{
		{"timeout", cmdflag.FlagTypeDuration, 0 * time.Millisecond, "timeout for command execution", nil},
	},
	Args: cmdflag.PredictNothing, // Technically, the could be a remote name.
}

var subcommands = []*cmdflag.Command{
	cmdPush,
}

type ExitCode int

func (ec ExitCode) Error() string {
	return fmt.Sprintf("ExitCode %v", ec)
}

func main() {
	var timeout time.Duration
	defer func() {
		if r := recover(); r != nil {
			switch r.(type) {
			case ExitCode:
				os.Exit(int(r.(ExitCode)))
			}
			fmt.Println("Recovered in f", r)
			os.Exit(1)
		}
	}()

	cmdMain.BindFlagSet(map[string]interface{}{"timeout": &timeout})

	cmd, args := cmdflag.Parse(cmdMain, subcommands)

	ctx := context.Background()
	if timeout > 0 {
		nctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		ctx = nctx
	}

	cmd.Run(ctx, cmd, args)
}
