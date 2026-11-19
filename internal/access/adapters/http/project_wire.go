package accesshttp

import (
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// The generated create, update and response models share this anonymous wire
// object. Keep explicit fields rather than serializing the storage document.
type projectPrivileges = struct {
	CreateLocalRepository   *bool `json:"create_local_repository,omitempty"`
	CreateRemoteRepository  *bool `json:"create_remote_repository,omitempty"`
	CreateVirtualRepository *bool `json:"create_virtual_repository,omitempty"`
	DeleteLocalRepository   *bool `json:"delete_local_repository,omitempty"`
	DeleteRemoteRepository  *bool `json:"delete_remote_repository,omitempty"`
	DeleteVirtualRepository *bool `json:"delete_virtual_repository,omitempty"`
	IndexResources          *bool `json:"index_resources,omitempty"`
	ManageMembers           *bool `json:"manage_members,omitempty"`
	ManageRemoteRepository  *bool `json:"manage_remote_repository,omitempty"`
	ManageResources         *bool `json:"manage_resources,omitempty"`
}

func projectField[T any](stored T, requested *T) T {
	if requested != nil {
		return *requested
	}
	return stored
}

func mergeProjectPrivileges(p model.AdminPrivileges, b *projectPrivileges, creating bool) model.AdminPrivileges {
	if b == nil {
		return p
	}
	p.ManageMembers = projectField(p.ManageMembers, b.ManageMembers)
	p.IndexResources = projectField(p.IndexResources, b.IndexResources)
	p.ManageResources = projectField(p.ManageResources, b.ManageResources)
	p.ManageRemoteRepository = projectField(p.ManageRemoteRepository, b.ManageRemoteRepository)
	granular := b.CreateLocalRepository != nil || b.CreateRemoteRepository != nil || b.CreateVirtualRepository != nil ||
		b.DeleteLocalRepository != nil || b.DeleteRemoteRepository != nil || b.DeleteVirtualRepository != nil
	if granular {
		p.CreateLocalRepository = projectField(p.CreateLocalRepository, b.CreateLocalRepository)
		p.CreateRemoteRepository = projectField(p.CreateRemoteRepository, b.CreateRemoteRepository)
		p.CreateVirtualRepository = projectField(p.CreateVirtualRepository, b.CreateVirtualRepository)
		p.DeleteLocalRepository = projectField(p.DeleteLocalRepository, b.DeleteLocalRepository)
		p.DeleteRemoteRepository = projectField(p.DeleteRemoteRepository, b.DeleteRemoteRepository)
		p.DeleteVirtualRepository = projectField(p.DeleteVirtualRepository, b.DeleteVirtualRepository)
		return p
	}
	// An update that only changes members/indexing must not reset independently
	// configured granular grants. An explicit legacy repository flag resolves
	// that flag's own repository classes, as the authored request contract says.
	if creating || b.ManageResources != nil {
		p.CreateLocalRepository, p.DeleteLocalRepository = p.ManageResources, p.ManageResources
		p.CreateVirtualRepository, p.DeleteVirtualRepository = p.ManageResources, p.ManageResources
	}
	if creating || b.ManageRemoteRepository != nil {
		p.CreateRemoteRepository, p.DeleteRemoteRepository = p.ManageRemoteRepository, p.ManageRemoteRepository
	}
	return p
}

func projectFromCreate(b accessgen.ProjectCreateRequest) model.Project {
	p := model.Project{
		Key: b.ProjectKey, DisplayName: b.DisplayName, Description: httpkit.Deref(b.Description),
		StorageQuotaBytes: int64(projectField(-1, b.StorageQuotaBytes)),
		SoftLimit:         httpkit.Deref(b.SoftLimit), QuotaEmail: httpkit.Deref(b.StorageQuotaEmailNotification),
		AdminPrivileges: model.AdminPrivileges{
			ManageMembers: true, ManageResources: true, IndexResources: true, ManageRemoteRepository: true,
			CreateLocalRepository: true, CreateRemoteRepository: true, CreateVirtualRepository: true,
			DeleteLocalRepository: true, DeleteRemoteRepository: true, DeleteVirtualRepository: true,
		},
	}
	p.AdminPrivileges = mergeProjectPrivileges(p.AdminPrivileges, b.AdminPrivileges, true)
	return p
}

func projectFromUpdate(p model.Project, b accessgen.ProjectUpdateRequest) model.Project {
	p.DisplayName = projectField(p.DisplayName, b.DisplayName)
	p.Description = projectField(p.Description, b.Description)
	if b.StorageQuotaBytes != nil {
		p.StorageQuotaBytes = int64(*b.StorageQuotaBytes)
	}
	p.SoftLimit = projectField(p.SoftLimit, b.SoftLimit)
	p.QuotaEmail = projectField(p.QuotaEmail, b.StorageQuotaEmailNotification)
	p.AdminPrivileges = mergeProjectPrivileges(p.AdminPrivileges, b.AdminPrivileges, false)
	return p
}

func projectWire(p model.Project) accessgen.Project {
	a := p.AdminPrivileges
	quota := p.StorageQuotaBytes
	if quota == 0 {
		quota = -1
	}
	return accessgen.Project{
		ProjectKey: httpkit.PtrTo(p.Key), DisplayName: httpkit.PtrTo(p.DisplayName), Description: httpkit.PtrTo(p.Description),
		StorageQuotaBytes: httpkit.PtrTo(int(quota)), SoftLimit: httpkit.PtrTo(p.SoftLimit),
		StorageQuotaEmailNotification: httpkit.PtrTo(p.QuotaEmail),
		AdminPrivileges: &projectPrivileges{
			ManageMembers: httpkit.PtrTo(a.ManageMembers), IndexResources: httpkit.PtrTo(a.IndexResources),
			ManageResources: httpkit.PtrTo(a.ManageResources), ManageRemoteRepository: httpkit.PtrTo(a.ManageRemoteRepository),
			CreateLocalRepository: httpkit.PtrTo(a.CreateLocalRepository), DeleteLocalRepository: httpkit.PtrTo(a.DeleteLocalRepository),
			CreateRemoteRepository: httpkit.PtrTo(a.CreateRemoteRepository), DeleteRemoteRepository: httpkit.PtrTo(a.DeleteRemoteRepository),
			CreateVirtualRepository: httpkit.PtrTo(a.CreateVirtualRepository), DeleteVirtualRepository: httpkit.PtrTo(a.DeleteVirtualRepository),
		},
	}
}

func projectRoleWire(r model.Role) (accessgen.ProjectRole, error) {
	typ := accessgen.ProjectRoleType(r.Type)
	switch r.Type {
	case model.RoleAdmin:
		// The project response schema names Project Admin among PREDEFINED
		// roles. ADMIN remains the protected internal/global role type.
		typ = accessgen.ProjectRoleType(model.RolePredefined)
	case model.RolePredefined, model.RoleCustom, model.RoleCustomGlobal:
	default:
		return accessgen.ProjectRole{}, fmt.Errorf("unknown stored project role type: %w", kernel.ErrInvalid)
	}
	return accessgen.ProjectRole{
		Name: httpkit.PtrTo(r.Name), Type: &typ, Description: httpkit.PtrTo(r.Description),
		Actions:      httpkit.PtrTo(append([]string{}, r.Actions...)),
		Environments: httpkit.PtrTo(append([]string{}, r.Environments...)),
	}, nil
}

func projectUserWire(m model.ProjectMember) accessgen.ProjectUser {
	return accessgen.ProjectUser{Name: httpkit.PtrTo(m.Name), Roles: httpkit.PtrTo(append([]string{}, m.Roles...))}
}

func projectGroupWire(m model.ProjectMember) accessgen.ProjectGroup {
	return accessgen.ProjectGroup{Name: httpkit.PtrTo(m.Name), Roles: httpkit.PtrTo(append([]string{}, m.Roles...))}
}
