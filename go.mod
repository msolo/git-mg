module github.com/msolo/git-mg

go 1.13

require (
	github.com/mattn/go-isatty v0.0.10
	github.com/msolo/cmdflag v0.0.0-20190210200038-6764a396fb53
	github.com/msolo/go-bis/flock v0.0.0-20191101065341-2a5026438708
	github.com/msolo/go-bis/glug v0.0.0-20191130031305-a08461dd90e6
	github.com/msolo/jsonc v0.0.0-20200906194452-9ff75078c8c4
	github.com/pkg/errors v0.8.1
	github.com/tebeka/atexit v0.1.0
	golang.org/x/sync v0.0.0-20190911185100-cd5d95a43a6e
)

replace github.com/msolo/cmdflag => ../cmdflag
