package main

import (
	"context"
	"fmt"

	"time"

	"github.com/tebeka/atexit"

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

func exitOnError(err error) {
	if err != nil {
		atexit.Fatal(err)
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
	Name: "git-sync",
	UsageLong: `git-sync - a tool to sync working directories

git-sync uses SSH, rsync and git (and optionally watchman via
fsmonitor) to efficiently copy local working directory changes to a
mirrored copy on a different machine. The goal is generally to be able
to reliably sync (even on an LTE connection) within 500ms even if the 
repo contains > 250k files.

git-sync is potentially destructive to the target working directory - it will
clean, reset and checkout changes to ensure the source and destination
working directories are equivalent.

Config:
git-sync reads a few variables from the [sync] section of the git config:

sync.remote_name (default "sync")
  This determines the remote target to use for syncing changes.

sync.exclude_paths (default empty)
  A colon-delimited list of patterns that will be passed to git clean
  on the remote target.  This allows some remote data to persist, even
  if it does not exist on the source workdir.

git-sync uses the remote name to determine the SSH URL that is used as
the target for rsync operations.

If core.fsmonitor is configured it will be used to find changes quickly.
`,
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
	defer atexit.Exit(0)

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
