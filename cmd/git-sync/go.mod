module git-sync

require (
	github.com/amoghe/distillog v0.0.0-20180726233512-ae382b35b717
	github.com/mattn/go-isatty v0.0.4
	github.com/msolo/cmdflag v0.0.0-20181212201819-a7d2c87e9616
	github.com/msolo/git-mg/flock v0.0.0
	github.com/pkg/errors v0.8.0
	golang.org/x/sync v0.0.0-20181108010431-42b317875d0f
)

replace github.com/msolo/git-mg/flock => ../../flock
