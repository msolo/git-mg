package main

import (
	"context"
	"log"
	"os"
	"time"

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

func runPush(ctx context.Context, cmd *cmdflag.Command, args []string) {
	cfg, err := readConfigFromGit()
	if err != nil {
		log.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	_, err = fullSync(cfg, wd)
	if err != nil {
		log.Fatal(err)
	}
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

func main() {
	var timeout time.Duration

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
