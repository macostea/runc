package main

import "github.com/urfave/cli"

var (
	checkpointCommand cli.Command
	eventsCommand     cli.Command
	restoreCommand    cli.Command
	killCommand       cli.Command
	execCommand       cli.Command
	initCommand       cli.Command
	pauseCommand      cli.Command
	resumeCommand     cli.Command
	//psCommand         cli.Command
	updateCommand     cli.Command
)
