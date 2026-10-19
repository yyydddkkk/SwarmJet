// Package methodology resolves a Hive's methodology into the role and files a
// Cell needs. The round-4 methodology task owns the full parser, validation and
// storage integration; this package currently implements the shared contract
// (Resolve and Builtin) so the Cell runtime can be wired against it first.
package methodology

import (
	"errors"
	"fmt"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// ErrUnknownRole is returned when a role name is not declared by the manifest.
var ErrUnknownRole = errors.New("methodology: unknown role")

// Resolve returns a role's effective configuration. An empty roleName selects
// the manifest's defaultRole.
func Resolve(m domain.MethodologyManifest, roleName string) (domain.Role, error) {
	if roleName == "" {
		roleName = m.DefaultRole
	}
	if roleName == "" {
		return domain.Role{}, errors.New("methodology: manifest has no default role")
	}
	role, ok := m.Roles[roleName]
	if !ok {
		return domain.Role{}, fmt.Errorf("%w %q", ErrUnknownRole, roleName)
	}
	return role, nil
}

// Builtin is the platform's minimal fallback methodology: one worker role that
// may publish and message other Agents, and no attestation requirement.
func Builtin() (domain.MethodologyManifest, []domain.MethodologyFile) {
	return domain.MethodologyManifest{
		DefaultRole: "worker",
		Roles: map[string]domain.Role{
			"worker": {
				Description: "writes code and opens pull requests",
				Permissions: domain.RolePermissions{
					Publish: true,
					Message: true,
				},
			},
		},
		Merge: domain.MergePolicy{
			RequireChecks: true,
		},
	}, nil
}
