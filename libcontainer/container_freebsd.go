package libcontainer

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/opencontainers/runc/libcontainer/configs"
	"github.com/opencontainers/runc/libcontainer/utils"
)

type freebsdContainer struct {
	id                   string
	root                 string
	config               *configs.Config
	jailId               string
	initProcessPid       int
	initProcessStartTime string
	devPartition         string
	m                    sync.Mutex
	state                containerState
	created              time.Time
}

// State represents a running container's state
type State struct {
	BaseState

	JailId string `json:"jailid"`
	// Platform specific fields below here
	DevPart string `json:"devpart"`
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
	c.m.Lock()
	defer c.m.Unlock()
	return c.currentStatus()
}

func (c *freebsdContainer) State() (*State, error) {
	c.m.Lock()
	defer c.m.Unlock()
	return c.currentState()
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

func (c *freebsdContainer) markCreated() (err error) {
	c.created = time.Now().UTC()
	c.state = &createdState{
		c: c,
	}
	state, err := c.updateState()
	if err != nil {
		return err
	}
	// init process start time may be "" if init has not finished
	c.initProcessStartTime = state.InitProcessStartTime
	return nil
}

func (c *freebsdContainer) markRunning() (err error) {
	c.jailId = c.getJailId(c.id)
	pid, _ := c.getInitProcessPid(c.jailId)
	pidInt, _ := strconv.Atoi(pid)
	c.initProcessPid = pidInt

	c.state = &runningState{
		c: c,
	}
	if _, err := c.updateState(); err != nil {
		return err
	}
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
	if err := c.start(process); err != nil {
		if status == Stopped {
			c.deleteExecFifo()
		}
		return err
	}
	return nil
}

func (c *freebsdContainer) getJailId(jname string) string {
	cmd := exec.Command("/usr/sbin/jls", "jid", "name")
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return ""
	}
	result := strings.Split(out.String(), "\n")
	for i := range result {
		if len(result[i]) > 0 {
			line := strings.Split(result[i], " ")
			if line[1] == jname {
				return line[0]
			}
		}
	}
	return ""
}

func (c *freebsdContainer) isJailExisted(jname, jid string) bool {
	jid1 := c.getJailId(jname)
	if jid == jid1 {
		return true
	}
	return false
}

func (c *freebsdContainer) getInitProcessPid(jid string) (string, error) {
	if !c.isJailExisted(c.id, jid) {
		return "", fmt.Errorf("jail %s was destroyed", c.id)
	}
	cmd := exec.Command("/usr/sbin/jexec", jid, "/bin/cat", filepath.Join("/", initCmdPidFilename))
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

func (c *freebsdContainer) isInitProcessRunning(jid string) (bool, error) {
	pid, err := c.getInitProcessPid(jid)
	if err != nil {
		return false, err
	}
	cmd := exec.Command("/usr/sbin/jexec", jid, "/bin/ps", "-p", pid)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		fmt.Println(err)
		return false, nil
	}
	return true, nil

}

func (c *freebsdContainer) getInitProcessTime(jid string) (string, error) {
	pid, err := c.getInitProcessPid(jid)
	if err != nil {
		return "", err
	}
	isRunning, err := c.isInitProcessRunning(jid)
	if err != nil {
		return "", err
	}
	if !isRunning {
		return "", fmt.Errorf("init process does not exist")
	}
	cmd := exec.Command("/usr/sbin/jexec", jid, "/bin/ps", "-o", "lstart", pid)
	// The output should be like:
	// STARTED
	// Thu Jun  8 17:18:35 2017
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	s := strings.Split(out.String(), "\n")
	return s[1], nil
}

func (c *freebsdContainer) start(process *Process) error {
	var (
		preCmdBuf  bytes.Buffer
		cmdBuf     bytes.Buffer
		conf       string
		jailStart  string
		jailStop   string
		devRelPath string
		devAbsPath string
	)
	preCmdBuf.WriteString(fmt.Sprintf("echo $$ > /%s; /bin/echo 0 > /%s",
		initCmdPidFilename, execFifoFilename))
	for _, v := range process.Args {
		if cmdBuf.Len() > 0 {
			cmdBuf.WriteString(" ")
		}
		cmdBuf.WriteString(v)
	}
	jailStart = fmt.Sprintf("/bin/sh /etc/rc")
	jailStop = fmt.Sprintf("/bin/sh /etc/rc.shutdown")
	params := map[string]string{
		"exec.clean":    "true",
		"exec.start":    jailStart,
		"exec.stop":     jailStop,
		"host.hostname": c.id,
		"path":          c.config.Rootfs,
		"command":       fmt.Sprintf("%s ; %s", preCmdBuf.String(), cmdBuf.String()),
	}
	devRelPath = filepath.Join(c.config.Rootfs, "dev")
	if devDir, err := os.Stat(devRelPath); err == nil {
		if devDir.IsDir() {
			devAbsPath, _ = filepath.Abs(devRelPath)
			params["mount.devfs"] = "true"
			c.devPartition = devAbsPath
		}
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
	jidPath := filepath.Join(c.root, "jid")
	cmd := exec.Command("/usr/sbin/jail", "-J", jidPath, "-f", jailConfPath, "-c")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Println("Fail to execute jail -f %s -c", jailConfPath)
		return nil
	}
	c.markCreated()
	var (
		waitErr     error
		jailStarted = make(chan bool)
	)
	go func() {
		if err := cmd.Wait(); err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				waitErr = err
			}
		}
		jailStarted <- true
	}()
	<-jailStarted

	if waitErr != nil {
		c.state = &stoppedState{
			c: c,
		}
		return waitErr
	}
	return waitErr
}

func (c *freebsdContainer) Run(process *Process) (err error) {
	c.m.Lock()
	status, err := c.currentStatus()
	if err != nil {
		c.m.Unlock()
		return err
	}
	c.m.Unlock()
	var containerReady = make(chan bool)
	if status == Stopped {
		go func() {
			c.exec()
			containerReady <- true
		}()
	}
	errs := c.Start(process)
	if status == Stopped {
		<-containerReady
	}
	if errs != nil {
		return errs
	}
	return nil
}

func (c *freebsdContainer) execWrapper(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Println("Fail to exec %s", name)
		return nil
	}
	var (
		waitErr error
		done    = make(chan bool)
	)
	go func() {
		if err := cmd.Wait(); err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				waitErr = err
			}
		}
		done <- true
	}()
	<-done
	return waitErr
}

func (c *freebsdContainer) Destroy() error {
	c.m.Lock()
	defer c.m.Unlock()
	existJid := c.getJailId(c.id)
	if c.jailId != "" && existJid == c.jailId {
		if err := c.execWrapper("/usr/sbin/jail", "-r", c.jailId); err != nil {
			fmt.Println("Fail to stop jail")
		}
		if c.devPartition != "" {
			if err := c.execWrapper("/sbin/umount", c.devPartition); err != nil {
				fmt.Println("Fail to umount %s", c.devPartition)
			}
		}
		c.jailId = ""
	} else {
		fmt.Println("Error: no jail id or destroyed")
	}
	return c.state.destroy()
}

func (c *freebsdContainer) Signal(s os.Signal, all bool) error {
	existJid := c.getJailId(c.id)
	if c.jailId != "" && existJid == c.jailId {
		if all {
			if err := c.execWrapper("/usr/sbin/jexec", c.jailId, "/bin/kill", "-KILL", "-1"); err != nil {
				fmt.Println("Fail to kill all processes")
			}
			c.jailId = ""
		} else {
			initPid := strconv.Itoa(c.initProcessPid)
			if err := c.execWrapper("/usr/sbin/jexec", c.jailId, "/bin/kill", "-KILL", initPid); err != nil {
				fmt.Println("Fail to kill all processes")
			}
		}
	} else {
		fmt.Println("Error: no jail id")
	}
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

	fifoName := filepath.Join(c.config.Rootfs, execFifoFilename)
	if _, err := os.Stat(fifoName); err == nil {
		c.deleteExecFifo()
		fmt.Errorf("exec fifo %s already exists", fifoName)
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
	fifoName := filepath.Join(c.config.Rootfs, execFifoFilename)
	os.Remove(fifoName)
}

func (c *freebsdContainer) Exec() error {
	c.m.Lock()
	defer c.m.Unlock()
	return c.exec()
}

func (c *freebsdContainer) exec() error {
	path := filepath.Join(c.config.Rootfs, execFifoFilename)
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return newSystemErrorWithCause(err, "open exec fifo for reading")
	}
	defer f.Close()
	// hold here util container writes something to the pipe,
	// which indicates the container is ready
	data, err := ioutil.ReadAll(f)
	if err != nil {
		return err
	}
	if len(data) > 0 {
		c.markRunning()
		os.Remove(path)
		return nil
	}
	return fmt.Errorf("cannot start an already running container")
}

// doesInitProcessExist checks if the init process is still the same process
// as the initial one, it could happen that the original process has exited
// and a new process has been created with the same pid, in this case, the
// container would already be stopped.
func (c *freebsdContainer) doesInitProcessExist() (bool, error) {
	isRunning, err := c.isInitProcessRunning(c.jailId)
	if !isRunning {
		return false, nil
	}
	startTime, err := c.getInitProcessTime(c.jailId)
	if err != nil {
		return false, newSystemErrorWithCause(err, "getting container start time")
	}
	if c.initProcessStartTime != startTime {
		return false, nil
	}
	return true, nil
}

func (c *freebsdContainer) runType() (Status, error) {
	if c.jailId == "" || !c.isJailExisted(c.id, c.jailId) {
		return Stopped, nil
	}
	// check if the process is still the original init process.
	exist, err := c.doesInitProcessExist()
	if !exist || err != nil {
		return Stopped, err
	}
	// We'll create exec fifo and blocking on it after container is created,
	// and delete it after start container.
	if _, err := os.Stat(filepath.Join(c.config.Rootfs, execFifoFilename)); err == nil {
		return Created, nil
	}
	return Running, nil
}

func (c *freebsdContainer) updateState() (*State, error) {
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
		startTime string
		pidInt    int
	)
	if c.jailId != "" {
		pidInt = c.initProcessPid
		if pidInt == 0 {
			pid, _ := c.getInitProcessPid(c.jailId)
			pidInt, _ := strconv.Atoi(pid)
			c.initProcessPid = pidInt
		}
		if c.initProcessStartTime == "" {
			startTime, _ = c.getInitProcessTime(c.jailId)
		} else {
			startTime = c.initProcessStartTime
		}
	}
	state := &State{
		BaseState: BaseState{
			ID:                   c.ID(),
			Config:               *c.config,
			InitProcessPid:       pidInt,
			InitProcessStartTime: startTime,
			Created:              c.created,
		},
		JailId:   c.jailId,
		DevPart:  c.devPartition,
		Rootless: c.config.Rootless,
	}
	return state, nil
}

func (c *freebsdContainer) currentStatus() (Status, error) {
	if err := c.refreshState(); err != nil {
		fmt.Println("CurrentStatus error")
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
