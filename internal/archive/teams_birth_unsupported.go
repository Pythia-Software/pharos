//go:build !darwin && !linux

package archive

import (
	"context"
	"errors"
)

var errTeamsBirthUnsupported = errors.New("fresh managed library creation is not available on this platform")
var teamsBirthHardwareUUID = func() (string, error) { return "", errTeamsBirthUnsupported }

func teamsEnsureManagedParent(string) error { return errTeamsBirthUnsupported }
func teamsCreateManagedLibrary(context.Context, string, string, string) (Config, error) {
	return Config{}, errTeamsBirthUnsupported
}
func teamsOpenManagedCatalog(context.Context, Config) (*Catalog, bool, error) { return nil, false, nil }

func teamsHasRetainedBirth(Config) bool { return false }
