package libcontainer

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"

	"github.com/opencontainers/runc/libcontainer/system"
)

/*
type parentProcess interface {
	// pid returns the pid for the running process.
	pid() int

	// start starts the process execution.
	start() error

	// send a SIGKILL to the process and wait for the exit.
	terminate() error

	// wait waits on the process returning the process state.
	wait() (*os.ProcessState, error)

	// startTime returns the process start time.
	startTime() (string, error)

	signal(os.Signal) error

	externalDescriptors() []string

	setExternalDescriptors(fds []string)
}
*/
type initProcess struct {
	cmd        *exec.Cmd
	parentPipe *os.File
	childPipe  *os.File
	//config        *initConfig
	container     *freebsdContainer
	fds           []string
	process       *Process
	bootstrapData io.Reader
	sharePidns    bool
	rootDir       *os.File
}

func (p *initProcess) pid() int {
	return p.cmd.Process.Pid
}

func (p *initProcess) externalDescriptors() []string {
	return p.fds
}

/*
// execSetns runs the process that executes C code to perform the setns calls
// because setns support requires the C process to fork off a child and perform the setns
// before the go runtime boots, we wait on the process to die and receive the child's pid
// over the provided pipe.
// This is called by initProcess.start function
func (p *initProcess) execSetns() error {
	status, err := p.cmd.Process.Wait()
	if err != nil {
		p.cmd.Wait()
		return err
	}
	if !status.Success() {
		p.cmd.Wait()
		return &exec.ExitError{ProcessState: status}
	}
	var pid *pid
	if err := json.NewDecoder(p.parentPipe).Decode(&pid); err != nil {
		p.cmd.Wait()
		return err
	}
	process, err := os.FindProcess(pid.Pid)
	if err != nil {
		return err
	}
	p.cmd.Process = process
	p.process.ops = p
	return nil
}
*/

func (p *initProcess) wait() (*os.ProcessState, error) {
	err := p.cmd.Wait()
	if err != nil {
		return p.cmd.ProcessState, err
	}
	/*
		// we should kill all processes in cgroup when init is died if we use host PID namespace
		if p.sharePidns {
			signalAllProcesses(p.manager, syscall.SIGKILL)
		}
	*/
	return p.cmd.ProcessState, nil
}

func (p *initProcess) terminate() error {
	if p.cmd.Process == nil {
		return nil
	}
	err := p.cmd.Process.Kill()
	if _, werr := p.wait(); err == nil {
		err = werr
	}
	return err
}

func (p *initProcess) startTime() (string, error) {
	return system.GetProcessStartTime(p.pid())
}

/*
func (p *initProcess) sendConfig() error {
	// send the config to the container's init process, we don't use JSON Encode
	// here because there might be a problem in JSON decoder in some cases, see:
	// https://github.com/docker/docker/issues/14203#issuecomment-174177790
	return utils.WriteJSON(p.parentPipe, p.config)
}
*/
func (p *initProcess) signal(sig os.Signal) error {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return errors.New("os: unsupported signal type")
	}
	return syscall.Kill(p.pid(), s)
}

func (p *initProcess) setExternalDescriptors(newFds []string) {
	p.fds = newFds
}
