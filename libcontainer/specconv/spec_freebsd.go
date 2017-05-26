// Package specconv implements conversion of specifications to libcontainer
// configurations
package specconv

import (
	"os"

	"github.com/opencontainers/runc/libcontainer/configs"
	"github.com/opencontainers/runtime-spec/specs-go"
)

type CreateOpts struct {
	Spec             *specs.Spec
}


// given specification and a cgroup name
func CreateLibcontainerConfig(opts *CreateOpts) (*configs.Config, error) {
	// runc's cwd will always be the bundle path
	_, err := os.Getwd()
	return nil, err
}
