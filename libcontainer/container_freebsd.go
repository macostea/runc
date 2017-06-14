package libcontainer

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/opencontainers/runc/libcontainer/configs"
	"github.com/opencontainers/runc/libcontainer/system"
	"github.com/opencontainers/runc/libcontainer/utils"
)

type freebsdContainer struct {
	id                   string
	root                 string
	config               *configs.Config
	initProcess          parentProcess
	initProcessStartTime string
	m                    sync.Mutex
	state                containerState
	created              time.Time
}

// State represents a running container's state
type State struct {
	BaseState

	// Platform specific fields below here

	// Specifies if the container was started under the rootless mode.
	Rootless bool `json:"rootless"`
}

// A libcontainer container object.
//
// Each container is thread-safe within the same process. Since a container can
// be destroyed by a separate process, any function may return that the container
// was not found.
type Container interface {
	BaseContainer

	// Methods below here are platform specific

}

func (c *freebsdContainer) ID() string {
	return c.id
}

func (c *freebsdContainer) Status() (Status, error) {
	return 0, nil
}

func (c *freebsdContainer) State() (*State, error) {
	return nil, nil
}

func (c *freebsdContainer) Config() configs.Config {
	return *c.config
}

func (c *freebsdContainer) Processes() ([]int, error) {
	return nil, nil
}

func (c *freebsdContainer) Stats() (*Stats, error) {
	return nil, nil
}

func (c *freebsdContainer) Set(config configs.Config) error {
	return nil
}

func (c *freebsdContainer) Start(process *Process) (err error) {
	c.m.Lock()
	defer c.m.Unlock()
	status, err := c.currentStatus()
	if err != nil {
		return err
	}
	if status == Stopped {
		if err := c.createExecFifo(); err != nil {
			return err
		}
	}
	if err := c.start(process, status == Stopped); err != nil {
		if status == Stopped {
			c.deleteExecFifo()
		}
		return err
	}
	return nil
}

func (c *freebsdContainer) start(process *Process, isInit bool) error {
	// generate a timestamp indicating when the container was started
	c.created = time.Now().UTC()

	var (
		cmdBuf bytes.Buffer
		conf string
	)
	for _, v := range process.Args {
		if cmdBuf.Len() > 0 {
			cmdBuf.WriteString(" ")
		}
		cmdBuf.WriteString(v)
	}
	params := map[string]string {
		"exec.clean":"true",
		"exec.start": "/bin/sh /etc/rc",
		"exec.stop": "/bin/sh /etc/rc.shutdown",
		"host.hostname": c.id,
		"path": c.config.Rootfs,
		"command": cmdBuf.String(),
	}
	lines := make([]string, 0, len(params))
	for k, v := range params {
		lines = append(lines, fmt.Sprintf("	%v=%#v;", k, v))
	}
	sort.Strings(lines)
	conf = fmt.Sprintf("%v {\n%v\n}\n", c.id, strings.Join(lines, "\n"))
	jailConfPath := filepath.Join(c.root, "jail.conf")
	if _, err := os.Stat(jailConfPath); err == nil {
		os.Remove(jailConfPath)
	}
	if err := ioutil.WriteFile(jailConfPath, []byte(conf), 0400); err != nil {
		fmt.Println("Fail to create jail conf %s", jailConfPath)
		return nil
	}
	// timeout after 5s
	ctx, cancel := context.WithTimeout(context.Background(), 5000 * time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/jail", "-f", jailConfPath, "-c")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Println("Fail to execute jail -f %s -c", jailConfPath)
		return nil
	}
	var (
		waitErr error
		waitLock = make(chan struct{})
	)
	go func() {
		if err := cmd.Wait(); err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				waitErr = err
			}
		}
		close(waitLock)
		c.state = &runningState{
			c: c,
		}
	}()
	<-waitLock
	return nil
}

func (c *freebsdContainer) Run(process *Process) (err error) {
	c.m.Lock()
	status, err := c.currentStatus()
	if err != nil {
		c.m.Unlock()
		return err
	}
	c.m.Unlock()
	if err := c.Start(process); err != nil {
		return err
	}
	if status == Stopped {
		//return c.exec()
	}
	return nil
}

func (c *freebsdContainer) Destroy() error {
	return nil
}

func (c *freebsdContainer) Signal(s os.Signal, all bool) error {
	return nil
}

func (c *freebsdContainer) createExecFifo() error {
	rootuid, err := c.Config().HostRootUID()
	if err != nil {
		return err
	}
	rootgid, err := c.Config().HostRootGID()
	if err != nil {
		return err
	}

	fifoName := filepath.Join(c.root, execFifoFilename)
	if _, err := os.Stat(fifoName); err == nil {
		return fmt.Errorf("exec fifo %s already exists", fifoName)
	}
	oldMask := syscall.Umask(0000)
	if err := syscall.Mkfifo(fifoName, 0622); err != nil {
		syscall.Umask(oldMask)
		return err
	}
	syscall.Umask(oldMask)
	if err := os.Chown(fifoName, rootuid, rootgid); err != nil {
		return err
	}
	return nil
}

func (c *freebsdContainer) deleteExecFifo() {
	fifoName := filepath.Join(c.root, execFifoFilename)
	os.Remove(fifoName)
}

func (c *freebsdContainer) Exec() error {
	c.m.Lock()
	defer c.m.Unlock()
	return c.exec()
}

func (c *freebsdContainer) exec() error {
	path := filepath.Join(c.root, execFifoFilename)
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return newSystemErrorWithCause(err, "open exec fifo for reading")
	}
	defer f.Close()
	data, err := ioutil.ReadAll(f)
	if err != nil {
		return err
	}
	if len(data) > 0 {
		os.Remove(path)
		return nil
	}
	return fmt.Errorf("cannot start an already running container")
}

// doesInitProcessExist checks if the init process is still the same process
// as the initial one, it could happen that the original process has exited
// and a new process has been created with the same pid, in this case, the
// container would already be stopped.
func (c *freebsdContainer) doesInitProcessExist(initPid int) (bool, error) {
	startTime, err := system.GetProcessStartTime(initPid)
	if err != nil {
		return false, newSystemErrorWithCausef(err, "getting init process %d start time", initPid)
	}
	if c.initProcessStartTime != startTime {
		return false, nil
	}
	return true, nil
}

func (c *freebsdContainer) runType() (Status, error) {
	if c.initProcess == nil {
		return Stopped, nil
	}
	pid := c.initProcess.pid()
	// return Running if the init process is alive
	if err := syscall.Kill(pid, 0); err != nil {
		if err == syscall.ESRCH {
			// It means the process does not exist anymore, could happen when the
			// process exited just when we call the function, we should not return
			// error in this case.
			return Stopped, nil
		}
		return Stopped, newSystemErrorWithCausef(err, "sending signal 0 to pid %d", pid)
	}
	// check if the process is still the original init process.
	exist, err := c.doesInitProcessExist(pid)
	if !exist || err != nil {
		return Stopped, err
	}
	// We'll create exec fifo and blocking on it after container is created,
	// and delete it after start container.
	if _, err := os.Stat(filepath.Join(c.root, execFifoFilename)); err == nil {
		return Created, nil
	}
	return Running, nil
}

func (c *freebsdContainer) updateState(process parentProcess) (*State, error) {
	c.initProcess = process
	state, err := c.currentState()
	if err != nil {
		return nil, err
	}
	err = c.saveState(state)
	if err != nil {
		return nil, err
	}
	return state, nil
}

func (c *freebsdContainer) saveState(s *State) error {
	f, err := os.Create(filepath.Join(c.root, stateFilename))
	if err != nil {
		return err
	}
	defer f.Close()
	return utils.WriteJSON(f, s)
}

func (c *freebsdContainer) deleteState() error {
	return os.Remove(filepath.Join(c.root, stateFilename))
}

func (c *freebsdContainer) isPaused() (bool, error) {
	// TODO
	return false, nil
}

func (c *freebsdContainer) currentState() (*State, error) {
	var (
		startTime           string
		pid                 = -1
	)
	if c.initProcess != nil {
		pid = c.initProcess.pid()
		startTime, _ = c.initProcess.startTime()
		//externalDescriptors = c.initProcess.externalDescriptors()
	}
	state := &State{
		BaseState: BaseState{
			ID:                   c.ID(),
			Config:               *c.config,
			InitProcessPid:       pid,
			InitProcessStartTime: startTime,
			Created:              c.created,
		},
		Rootless:            c.config.Rootless,
	}
	return state, nil
}


func (c *freebsdContainer) currentStatus() (Status, error) {
	if err := c.refreshState(); err != nil {
		return -1, err
	}
	return c.state.status(), nil
}

// refreshState needs to be called to verify that the current state on the
// container is what is true.  Because consumers of libcontainer can use it
// out of process we need to verify the container's status based on runtime
// information and not rely on our in process info.
func (c *freebsdContainer) refreshState() error {
	paused, err := c.isPaused()
	if err != nil {
		return err
	}
	if paused {
		return c.state.transition(&pausedState{c: c})
	}
	t, err := c.runType()
	if err != nil {
		return err
	}
	switch t {
	case Created:
		return c.state.transition(&createdState{c: c})
	case Running:
		return c.state.transition(&runningState{c: c})
	}
	return c.state.transition(&stoppedState{c: c})
}
