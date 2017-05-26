package libcontainer

import (
	"fmt"
	"os"

	"github.com/opencontainers/runc/libcontainer/configs"
)

type FreeBSDFactory struct {
	// Root directory for the factory to store state.
	Root string
}

func New(root string, options ...func(*FreeBSDFactory) error) (Factory, error) {
	if root != "" {
		if err := os.MkdirAll(root, 0700); err != nil {
			return nil, newGenericError(err, SystemError)
		}
	}

	l := &FreeBSDFactory{
		Root: root,
	}

	return l, nil
}

func (l *FreeBSDFactory) Create(id string, config *configs.Config) (Container, error) {
	if l.Root == "" {
		return nil, newGenericError(fmt.Errorf("invalid root"), ConfigInvalid)
	}

	c := &freebsdContainer{
		id: id,
	}

	return c, nil
}

func (l *FreeBSDFactory) Load(id string) (Container, error) {
	if l.Root == "" {
		return nil, newGenericError(fmt.Errorf("invalid root"), ConfigInvalid)
	}
	return nil, nil
}

func (l *FreeBSDFactory) Type() string {
	return "libcontainer"
}

// StartInitialization loads a container by opening the pipe fd from the parent to read the configuration and state
// This is a low level implementation detail of the reexec and should not be consumed externally
func (l *FreeBSDFactory) StartInitialization() (err error) {
	return nil
}
