package main

import (
	"context"
	"os"

	"time"

	log "github.com/msolo/go-bis/glug"
	"github.com/tebeka/atexit"

	"github.com/msolo/cmdflag"
)

var cmdPush = &cmdflag.Command{
	Name:      "push",
	Run:       runPush,
	UsageLine: "push <remote name>",
	UsageLong: `Push a working directory to a remote working dir.`,
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

git-sync is potentially destructive to the target working directory -
it will clean, reset and checkout changes to ensure the source and
destination working directories are equivalent.

Config:
git-sync reads a few variables from the [sync] section of the git config:

sync.remoteName (default "sync")
  This determines the remote target to use for syncing changes.

sync.excludePaths (default empty)
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

func main() {
	defer atexit.Exit(0)

	if val := os.Getenv("GIT_TRACE_PERFORMANCE"); val != "" && val != "0" {
		log.SetLevel("INFO")
	} else {
		log.SetLevel("WARNING")
	}

	var timeout time.Duration
	fs := cmdMain.BindFlagSet(map[string]interface{}{"timeout": &timeout})
	log.RegisterFlags(fs)
	RegisterFlags(fs)

	cmd, args := cmdflag.Parse(cmdMain, subcommands)

	ctx := context.Background()
	if timeout > 0 {
		nctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		ctx = nctx
	}

	cmd.Run(ctx, cmd, args)
}
