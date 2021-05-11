package containerrun

//go:generate mockgen -destination=./mocks/mock_containerrun.go -package=mocks code.cloudfoundry.org/quarks-container-run/pkg/containerrun Runner,Checker,Process,OSProcess,ExecCommandContext,PacketListener,PacketConnection
//go:generate mockgen -destination=./mocks/mock_context.go -package=mocks context Context

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	postStartTimeout   = time.Minute * 15
	conditionSleepTime = time.Second * 3
	sigtermTimeout     = time.Second * 20

	// ProcessStart is the command to restart the suspended child processes.
	ProcessStart = "START"
	// ProcessStop is the command to stop and suspend the child processes.
	ProcessStop = "STOP"
	// SignalQuit is the command to send a QUIT signal to the child processes.
	SignalQuit = "QUIT"
)

type processCommand string

// CmdRun represents the signature for the top-level Run command.
type CmdRun func(
	runner Runner,
	conditionRunner Runner,
	commandChecker Checker,
	listener PacketListener,
	stdio Stdio,
	args []string,
	jobName string,
	processName string,
	postStartCommandName string,
	postStartCommandArgs []string,
	postStartConditionCommandName string,
	postStartConditionCommandArgs []string,
) error

func Run(
	runner Runner,
	conditionRunner Runner,
	commandChecker Checker,
	listener PacketListener,
	stdio Stdio,
	args []string,
	jobName string,
	processName string,
	postStartCommandName string,
	postStartCommandArgs []string,
	postStartConditionCommandName string,
	postStartConditionCommandArgs []string,
) error {
	return RunWithTestChan(runner, conditionRunner, commandChecker, listener,
		stdio, args, jobName, processName,
		postStartCommandName, postStartCommandArgs, postStartConditionCommandName,
		postStartConditionCommandArgs, nil)
}

// Run implements the logic for the container-run CLI command.
func RunWithTestChan(
	runner Runner,
	conditionRunner Runner,
	commandChecker Checker,
	listener PacketListener,
	stdio Stdio,
	args []string,
	jobName string,
	processName string,
	postStartCommandName string,
	postStartCommandArgs []string,
	postStartConditionCommandName string,
	postStartConditionCommandArgs []string,
	testSigTermChan chan struct{},
) error {
	if len(args) == 0 {
		err := fmt.Errorf("a command is required")
		return &runErr{err}
	}

	done := make(chan struct{}, 1)
	sigterm := make(chan struct{}, 1)
	if testSigTermChan != nil {
		sigterm = testSigTermChan
	}
	errors := make(chan error)
	sigs := make(chan os.Signal, 1)
	commands := make(chan processCommand)

	signal.Notify(sigs, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)
	processRegistry := NewProcessRegistry()

	command := Command{
		Name: args[0],
		Arg:  args[1:],
	}
	conditionCommand := Command{
		Name: postStartConditionCommandName,
		Arg:  postStartConditionCommandArgs,
	}
	postStartCommand := Command{
		Name: postStartCommandName,
		Arg:  postStartCommandArgs,
	}

	if err := startProcesses(
		jobName,
		processName,
		runner,
		conditionRunner,
		commandChecker,
		stdio,
		command,
		postStartCommand,
		conditionCommand,
		processRegistry,
		errors,
		done,
	); err != nil {
		return err
	}

	go processRegistry.HandleSignals(sigs, sigterm, errors)

	// This flag records the state of the system and its child
	// processes. It is set to true when the child processes are
	// running, and false otherwise.
	active := true

	if err := watchForCommands(listener, jobName, processName, errors, commands); err != nil {
		return err
	}

	for {
		select {
		case cmd := <-commands:
			log.Debugf("Received %s command\n", cmd)
			// Note: Commands are ignored if the system is
			// already in the requested state. I.e
			// demanding things to stop when things are
			// already stopped does nothing. Similarly for
			// demanding a start when the children are
			// started/up/active.
			switch cmd {
			case ProcessStop:
				if active {
					// Order is important here.
					// The `stopProcesses` sends
					// signals to the children,
					// which unlocks their Wait,
					// which causes events to be
					// posted on the done channel.
					// To properly ignore these
					// events the flag has to be
					// set before sending any
					// signals.

					active = false
					// Run asynchronously so we can receive errors
					go stopProcesses(processRegistry, errors)
				}
			case ProcessStart:
				if !active {
					// Make sure any previous instance is gone now, and the kill timer is stopped.
					processRegistry.KillAll()

					err := startProcesses(
						jobName,
						processName,
						runner,
						conditionRunner,
						commandChecker,
						stdio,
						command,
						postStartCommand,
						conditionCommand,
						processRegistry,
						errors,
						done)
					if err != nil {
						return err
					}

					active = true
				}
			case SignalQuit:
				processRegistry.SignalAll(syscall.SIGQUIT)
			}
		case <-done:
			// When the main process returns without error, treat it the
			// same as if it has been stopped.
			active = false
		case <-sigterm:
			log.Debugln("Waiting for all children to stop")
			// Once we receive a SIGTERM we wait until all child processes have terminated
			// because Kubernetes will kill the container once the main process exits.
			for {
				if processRegistry.Count() == 0 {
					return nil
				}
				time.Sleep(1 * time.Second)
			}
		case err := <-errors:
			// Ignore done signals when we actively
			// stopped the children via ProcessStop.
			// Wait returns with !state.Success, `signal: killed`
			if active {
				return err
			}
		}
	}
}

func watchForCommands(
	listener PacketListener,
	jobName, processName string,
	errors chan error,
	commands chan processCommand,
) error {
	sockAddr := fmt.Sprintf("/var/vcap/data/%s/%s_containerrun.sock", jobName, processName)

	go func() {
		for {
			if err := os.RemoveAll(sockAddr); err != nil {
				errors <- fmt.Errorf("failed to setup command socket: %v", err)
				return
			}

			// Accept new packet, dispatching them to our handler
			packet, err := listener.ListenPacket("unixgram", sockAddr)
			if err != nil {
				errors <- fmt.Errorf("failed to watch for commands: %v", err)
			}
			if packet != nil {
				handlePacket(packet, errors, commands)
			}
		}
	}()

	return nil
}

func handlePacket(
	conn PacketConnection,
	errors chan error,
	commands chan processCommand,
) {
	defer conn.Close()

	packet := make([]byte, 256)
	n, _, err := conn.ReadFrom(packet)
	// Return address ignored. We do not send anything out.
	if err != nil && err != io.EOF {
		errors <- fmt.Errorf("failed to read command: %v", err)
	}

	command := strings.TrimSpace(string(packet[:n]))
	switch command {
	case ProcessStart, ProcessStop, SignalQuit:
		commands <- processCommand(command)
	default:
		// Bad commands are ignored. Else they could be used to DOS the runner.
	}
}

func stopProcesses(processRegistry *ProcessRegistry, errors chan<- error) {
		log.Debugln("sending SIGTERM")
		for _, err := range processRegistry.SignalAll(syscall.SIGTERM) {
			errors <- err
		}
		// bpm would send a SIGQUIT signal to dump the stack before sending SIGKILL,
		// but there doesn't seem to be a point to be doing it in this context.
		processRegistry.timer = time.AfterFunc(sigtermTimeout, func() {
			log.Debugln("timeout SIGTERM")
			processRegistry.KillAll()
		})
}

func startProcesses(
	jobName string,
	processName string,
	runner Runner,
	conditionRunner Runner,
	commandChecker Checker,
	stdio Stdio,
	command Command,
	postStartCommand Command,
	conditionCommand Command,
	processRegistry *ProcessRegistry,
	errors chan error,
	done chan struct{},
) error {
	if err := startMainProcess(
		jobName,
		processName,
		runner,
		command,
		stdio,
		processRegistry,
		errors,
		done,
	); err != nil {
		return err
	}

	startPostStartProcesses(
		runner,
		conditionRunner,
		commandChecker,
		stdio,
		postStartCommand,
		conditionCommand,
		processRegistry,
		errors)

	return nil
}

func startMainProcess(
	jobName string,
	processName string,
	runner Runner,
	command Command,
	stdio Stdio,
	processRegistry *ProcessRegistry,
	errors chan error,
	done chan struct{},
) error {
	process, err := runner.Run(command, stdio)
	if err != nil {
		return &runErr{err}
	}
	processRegistry.Register(process)

	sentinel := fmt.Sprintf("/var/vcap/data/%s/%s_containerrun.running", jobName, processName)
	file, _ := os.Create(sentinel)
	_ = file.Close()

	go func() {
		if err := process.Wait(); err != nil {
			log.Debugf("Process has failed with error: %s\n", err)
			processRegistry.Unregister(process)
			os.Remove(sentinel)
			errors <- &runErr{err}
			return
		}
		log.Debugln("Process has ended normally")
		processRegistry.Unregister(process)
		os.Remove(sentinel)
		done <- struct{}{}
	}()

	return nil
}

func startPostStartProcesses(
	runner Runner,
	conditionRunner Runner,
	commandChecker Checker,
	stdio Stdio,
	postStartCommand Command,
	conditionCommand Command,
	processRegistry *ProcessRegistry,
	errors chan error,
) {
	if postStartCommand.Name != "" {
		if commandChecker.Check(postStartCommand.Name) {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), postStartTimeout)
				defer cancel()

				if conditionCommand.Name != "" {
					conditionStdio := Stdio{
						Out: ioutil.Discard,
						Err: ioutil.Discard,
					}

					if _, err := conditionRunner.RunContext(ctx, conditionCommand, conditionStdio); err != nil {
						errors <- &runErr{err}
						return
					}
				}

				postStartProcess, err := runner.RunContext(ctx, postStartCommand, stdio)
				if err != nil {
					errors <- &runErr{err}
					return
				}
				processRegistry.Register(postStartProcess)
				defer processRegistry.Unregister(postStartProcess)
				if err := postStartProcess.Wait(); err != nil {
					errors <- &runErr{err}
					return
				}
			}()
		}
	}
}

type runErr struct {
	err error
}

func (e *runErr) Error() string {
	return fmt.Sprintf("failed to run container: %v", e.err)
}

// Command represents a command to be run.
type Command struct {
	Name string
	Arg  []string
}

// Runner is the interface that wraps the Run methods.
type Runner interface {
	Run(command Command, stdio Stdio) (Process, error)
	RunContext(ctx context.Context, command Command, stdio Stdio) (Process, error)
}

// ContainerRunner satisfies the Runner interface.
type ContainerRunner struct {
}

// NewContainerRunner constructs a new ContainerRunner.
func NewContainerRunner() *ContainerRunner {
	return &ContainerRunner{}
}

// Run runs a command async.
func (cr *ContainerRunner) Run(
	command Command,
	stdio Stdio,
) (Process, error) {
	cmd := exec.Command(command.Name, command.Arg...)
	return cr.run(cmd, stdio)
}

// RunContext runs a command async with a context.
func (cr *ContainerRunner) RunContext(
	ctx context.Context,
	command Command,
	stdio Stdio,
) (Process, error) {
	cmd := exec.CommandContext(ctx, command.Name, command.Arg...)
	return cr.run(cmd, stdio)
}

func (cr *ContainerRunner) run(
	cmd *exec.Cmd,
	stdio Stdio,
) (Process, error) {
	cmd.Stdout = stdio.Out
	cmd.Stderr = stdio.Err
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to run command: %v", err)
	}
	return NewContainerProcess(cmd.Process), nil
}

// ConditionRunner satisfies the Runner interface. It represents a runner for a post-start
// pre-condition.
type ConditionRunner struct {
	sleep              func(time.Duration)
	execCommandContext func(context.Context, string, ...string) *exec.Cmd
}

// NewConditionRunner constructs a new ConditionRunner.
func NewConditionRunner(
	sleep func(time.Duration),
	execCommandContext func(context.Context, string, ...string) *exec.Cmd,
) *ConditionRunner {
	return &ConditionRunner{
		sleep:              sleep,
		execCommandContext: execCommandContext,
	}
}

// Run is not implemented.
func (cr *ConditionRunner) Run(
	command Command,
	stdio Stdio,
) (Process, error) {
	panic("not implemented")
}

// RunContext runs a condition until it succeeds or the context times out. The process is never
// returned. A context timeout makes RunContext to return the error.
func (cr *ConditionRunner) RunContext(
	ctx context.Context,
	command Command,
	_ Stdio,
) (Process, error) {
	for {
		cr.sleep(conditionSleepTime)
		cmd := cr.execCommandContext(ctx, command.Name, command.Arg...)
		if err := cmd.Run(); err != nil {
			if err := ctx.Err(); err == context.DeadlineExceeded {
				return nil, err
			}
			continue
		}
		break
	}

	return nil, nil
}

// Process is the interface that wraps the Signal and Wait methods of a process.
type Process interface {
	Signal(os.Signal) error
	Wait() error
}

// OSProcess is the interface that wraps the methods for *os.Process.
type OSProcess interface {
	Signal(os.Signal) error
	Wait() (*os.ProcessState, error)
}

// ContainerProcess satisfies the Process interface.
type ContainerProcess struct {
	process OSProcess
}

// NewContainerProcess constructs a new ContainerProcess.
func NewContainerProcess(process OSProcess) *ContainerProcess {
	return &ContainerProcess{
		process: process,
	}
}

// Signal sends a signal to the process. If the process is not running anymore, it's no-op.
func (p *ContainerProcess) Signal(sig os.Signal) error {
	// A call to ContainerProcess.Signal is no-op if the process it handles is not running.
	if err := p.process.Signal(syscall.Signal(0)); err != nil {
		return nil
	}
	if err := p.process.Signal(sig); err != nil {
		return fmt.Errorf("failed to send signal to process: %v", err)
	}
	return nil
}

// Wait waits for the process.
func (p *ContainerProcess) Wait() error {
	state, err := p.process.Wait()
	if err != nil {
		return fmt.Errorf("failed to run process: %v", err)
	} else if !state.Success() {
		err := &exec.ExitError{ProcessState: state}
		return fmt.Errorf("failed to run process: %v", err)
	}
	return nil
}

// Stdio represents the STDOUT and STDERR to be used by a process.
type Stdio struct {
	Out io.Writer
	Err io.Writer
}

// ProcessRegistry handles all the processes.
type ProcessRegistry struct {
	processes []Process
	timer     *time.Timer
	sync.Mutex
}

// NewProcessRegistry constructs a new ProcessRegistry.
func NewProcessRegistry() *ProcessRegistry {
	return &ProcessRegistry{
		processes: make([]Process, 0),
		// It should always be safe to call timer.Stop(), so it must not be nil.
		timer: time.NewTimer(time.Millisecond),
	}
}

// Count returns the number of processes in the registry
func (pr *ProcessRegistry) Count() int {
	pr.Lock()
	defer pr.Unlock()

	return len(pr.processes)
}

// Register registers a process in the registry and returns how many processes are registered.
func (pr *ProcessRegistry) Register(p Process) int {
	pr.Lock()
	defer pr.Unlock()

	log.Debugf("Registering process %s\n", p)
	pr.processes = append(pr.processes, p)
	return len(pr.processes)
}

// Unregister removes a process from the registry and returns how many processes are still registered.
func (pr *ProcessRegistry) Unregister(p Process) int {
	pr.Lock()
	defer pr.Unlock()

	log.Debugf("Unregistering process %s\n", p)
	processes := make([]Process, 0, len(pr.processes))
	for _, process := range pr.processes {
		if p != process {
			processes = append(processes, process)
		}
	}
	pr.processes = processes
	if len(pr.processes) == 0 {
		pr.timer.Stop()
	}
	return len(pr.processes)
}

// SignalAll sends a signal to all registered processes.
func (pr *ProcessRegistry) SignalAll(sig os.Signal) []error {
	pr.Lock()
	defer pr.Unlock()
	log.Debugf("Sending '%s' signal to %d processes\n", sig, len(pr.processes))
	errors := make([]error, 0)
	for _, p := range pr.processes {
		if err := p.Signal(sig); err != nil {
			errors = append(errors, err)
		}
	}
	return errors
}

// KillAll stops the timer and sends a kill signal to all registered processes.
func (pr *ProcessRegistry) KillAll() {
	log.Debugln("KillAll")
	pr.timer.Stop()
	pr.SignalAll(os.Kill)
}

// HandleSignals handles the signals channel and forwards them to the
// registered processes. After a signal is handled it keeps running to
// handle any future ones.
func (pr *ProcessRegistry) HandleSignals(sigs <-chan os.Signal, sigterm chan<- struct{}, errors chan<- error) {
	for {
		sig := <-sigs
		log.Debugf("Received '%s' signal\n", sig)
		if sig == syscall.SIGTERM || sig == syscall.SIGINT {
			stopProcesses(pr, errors)
			log.Debugln("Write to sigterm channel")
			sigterm <- struct{}{}
		} else {
			for _, err := range pr.SignalAll(sig) {
				errors <- err
			}
		}
	}
}

// Checker is the interface that wraps the basic Check method.
type Checker interface {
	Check(command string) bool
}

// CommandChecker satisfies the Checker interface.
type CommandChecker struct {
	osStat       func(string) (os.FileInfo, error)
	execLookPath func(file string) (string, error)
}

// NewCommandChecker constructs a new CommandChecker.
func NewCommandChecker(
	osStat func(string) (os.FileInfo, error),
	execLookPath func(file string) (string, error),
) *CommandChecker {
	return &CommandChecker{
		osStat:       osStat,
		execLookPath: execLookPath,
	}
}

// Check checks if command exists as a file or in $PATH.
func (cc *CommandChecker) Check(command string) bool {
	_, statErr := cc.osStat(command)
	_, lookPathErr := cc.execLookPath(command)
	return statErr == nil || lookPathErr == nil
}

// ExecCommandContext wraps exec.CommandContext.
type ExecCommandContext interface {
	CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd
}

// PacketConnection is the interface that wraps the PacketConn methods.
type PacketConnection interface {
	ReadFrom(p []byte) (n int, addr net.Addr, err error)
	Close() error
}

// ListenPacketFunc is a type alias to the net.ListenPacket function.
type ListenPacketFunc func(network, address string) (net.PacketConn, error)

// PacketListener is the interface that wraps the ListenPacket methods.
// net.PacketConn satisfies this interface.
type PacketListener interface {
	ListenPacket(network, address string) (PacketConnection, error)
}

// NetPacketListener satisfies the PacketListener interface.
type NetPacketListener struct {
	listen ListenPacketFunc
}

// NewNetPacketListener constructs a new NetPacketListener.
func NewNetPacketListener(listen ListenPacketFunc) *NetPacketListener {
	return &NetPacketListener{
		listen: listen,
	}
}

// ListenPacket implements listening for packets.
func (npl *NetPacketListener) ListenPacket(network, address string) (PacketConnection, error) {
	conn, err := npl.listen(network, address)
	if err != nil {
		return nil, fmt.Errorf("failed to listen for packet: %v", err)
	}
	return conn, nil
}
