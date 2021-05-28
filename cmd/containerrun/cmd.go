package containerrun

import (
	"net"
	"os"
	"os/exec"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	pkg "code.cloudfoundry.org/quarks-container-run/pkg/containerrun"
)

// NewContainerRunCmd constructs a new container-run command.
func NewContainerRunCmd(
	run pkg.CmdRun,
	runner pkg.Runner,
	conditionRunner pkg.Runner,
	commandChecker pkg.Checker,
	listener pkg.PacketListener,
	stdio pkg.Stdio,
) *cobra.Command {
	var jobName string
	var processName string
	var postStartCommandName string
	var postStartCommandArgs []string
	var postStartConditionCommandName string
	var postStartConditionCommandArgs []string
	var debug bool
	var errand bool

	cmd := &cobra.Command{
		Use:           "container-run",
		Short:         "Runs a command and a post-start with optional conditions",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			if debug {
				log.SetLevel(log.DebugLevel)
			}
			return run(
				runner,
				conditionRunner,
				commandChecker,
				listener,
				stdio,
				args,
				jobName,
				processName,
				errand,
				postStartCommandName,
				postStartCommandArgs,
				postStartConditionCommandName,
				postStartConditionCommandArgs,
			)
		},
	}

	cmd.Flags().StringVar(&jobName, "job-name", "", "the controlling job")
	cmd.Flags().StringVar(&processName, "process-name", "", "the main process")
	cmd.Flags().StringVar(&postStartCommandName, "post-start-name", "", "the post-start command name")
	cmd.Flags().StringArrayVar(&postStartCommandArgs, "post-start-arg", []string{}, "a post-start command arg")
	cmd.Flags().StringVar(&postStartConditionCommandName, "post-start-condition-name", "", "the post-start condition command name")
	cmd.Flags().StringArrayVar(&postStartConditionCommandArgs, "post-start-condition-arg", []string{}, "a post-start condition command arg")
	cmd.Flags().BoolVar(&debug, "debug", false, "enable debug logging")
	cmd.Flags().BoolVar(&errand, "errand", false, "errands terminate container-run even when they exit with exit code 0")

	return cmd
}

// NewDefaultContainerRunCmd constructs a new container-run command with the default dependencies.
func NewDefaultContainerRunCmd() *cobra.Command {
	runner := pkg.NewContainerRunner()
	conditionRunner := pkg.NewConditionRunner(time.Sleep, exec.CommandContext)
	commandChecker := pkg.NewCommandChecker(os.Stat, exec.LookPath)
	listener := pkg.NewNetPacketListener(net.ListenPacket)
	stdio := pkg.Stdio{
		Out: os.Stdout,
		Err: os.Stderr,
	}
	return NewContainerRunCmd(pkg.Run, runner, conditionRunner, commandChecker, listener, stdio)
}
