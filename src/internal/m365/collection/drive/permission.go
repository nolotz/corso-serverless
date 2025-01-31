package drive

import (
	"context"
	"strings"

	"github.com/alcionai/clues"
	"github.com/microsoftgraph/msgraph-sdk-go/drives"
	"github.com/microsoftgraph/msgraph-sdk-go/models"

	"github.com/alcionai/corso/src/internal/common/ptr"
	"github.com/alcionai/corso/src/internal/common/syncd"
	"github.com/alcionai/corso/src/internal/data"
	"github.com/alcionai/corso/src/internal/m365/collection/drive/metadata"
	"github.com/alcionai/corso/src/internal/version"
	"github.com/alcionai/corso/src/pkg/fault"
	"github.com/alcionai/corso/src/pkg/logger"
	"github.com/alcionai/corso/src/pkg/path"
	"github.com/alcionai/corso/src/pkg/services/m365/api/graph"
)

// empty string is used to indicate that a permission cannot be restored
const nonRestorablePermission = ""

func getParentMetadata(
	parentPath path.Path,
	parentDirToMeta syncd.MapTo[metadata.Metadata],
) (metadata.Metadata, error) {
	parentMeta, ok := parentDirToMeta.Load(parentPath.String())
	if !ok {
		drivePath, err := path.ToDrivePath(parentPath)
		if err != nil {
			return metadata.Metadata{}, clues.Wrap(err, "invalid restore path")
		}

		if len(drivePath.Folders) != 0 {
			return metadata.Metadata{}, clues.Wrap(err, "computing item permissions")
		}

		parentMeta = metadata.Metadata{}
	}

	return parentMeta, nil
}

func getCollectionMetadata(
	ctx context.Context,
	drivePath *path.DrivePath,
	dc data.RestoreCollection,
	caches *restoreCaches,
	backupVersion int,
	restorePerms bool,
) (metadata.Metadata, error) {
	if !restorePerms || backupVersion < version.OneDrive1DataAndMetaFiles {
		return metadata.Metadata{}, nil
	}

	var (
		err      error
		fullPath = dc.FullPath()
	)

	if len(drivePath.Folders) == 0 {
		// No permissions for root folder
		return metadata.Metadata{}, nil
	}

	if backupVersion < version.OneDrive4DirIncludesPermissions {
		colMeta, err := getParentMetadata(fullPath, caches.ParentDirToMeta)
		if err != nil {
			return metadata.Metadata{}, clues.Wrap(err, "collection metadata")
		}

		return colMeta, nil
	}

	folders := fullPath.Folders()
	metaName := folders[len(folders)-1] + metadata.DirMetaFileSuffix

	if backupVersion >= version.OneDrive5DirMetaNoName {
		metaName = metadata.DirMetaFileSuffix
	}

	meta, err := FetchAndReadMetadata(ctx, dc, metaName)
	if err != nil {
		return metadata.Metadata{}, clues.Wrap(err, "collection metadata")
	}

	return meta, nil
}

// Unlike permissions, link shares are inherited from all the parents
// of an item and not just the direct parent.
func computePreviousLinkShares(
	ctx context.Context,
	originDir path.Path,
	parentMetas syncd.MapTo[metadata.Metadata],
) ([]metadata.LinkShare, error) {
	linkShares := []metadata.LinkShare{}
	ctx = clues.Add(ctx, "origin_dir", originDir)

	parent, err := originDir.Dir()
	if err != nil {
		return nil, clues.WrapWC(ctx, err, "getting parent")
	}

	for len(parent.Elements()) > 0 {
		ictx := clues.Add(ctx, "current_ancestor_dir", parent)

		drivePath, err := path.ToDrivePath(parent)
		if err != nil {
			return nil, clues.WrapWC(ictx, err, "transforming dir to drivePath")
		}

		if len(drivePath.Folders) == 0 {
			break
		}

		meta, ok := parentMetas.Load(parent.String())
		if !ok {
			return nil, clues.NewWC(ictx, "no metadata found in parent")
		}

		// Any change in permissions would change it to custom
		// permission set and so we can filter on that.
		if meta.SharingMode == metadata.SharingModeCustom {
			linkShares = append(linkShares, meta.LinkShares...)
		}

		parent, err = parent.Dir()
		if err != nil {
			return nil, clues.WrapWC(ictx, err, "getting parent")
		}
	}

	return linkShares, nil
}

// computePreviousMetadata computes the parent permissions by
// traversing parentMetas and finding the first item with custom
// permissions. parentMetas is expected to have all the parent
// directory metas for this to work.
func computePreviousMetadata(
	ctx context.Context,
	originDir path.Path,
	// map parent dir -> parent's metadata
	parentMetas syncd.MapTo[metadata.Metadata],
) (metadata.Metadata, error) {
	var (
		parent path.Path
		meta   metadata.Metadata

		err error
		ok  bool
	)

	parent = originDir

	for {
		parent, err = parent.Dir()
		if err != nil {
			return metadata.Metadata{}, clues.WrapWC(ctx, err, "getting parent")
		}

		ictx := clues.Add(ctx, "parent_dir", parent)

		drivePath, err := path.ToDrivePath(parent)
		if err != nil {
			return metadata.Metadata{}, clues.WrapWC(ictx, err, "transforming dir to drivePath")
		}

		if len(drivePath.Folders) == 0 {
			return metadata.Metadata{}, nil
		}

		meta, ok = parentMetas.Load(parent.String())
		if !ok {
			return metadata.Metadata{}, clues.NewWC(ictx, "no metadata found for parent folder: "+parent.String())
		}

		if meta.SharingMode == metadata.SharingModeCustom {
			return meta, nil
		}
	}
}

type updateDeleteItemPermissioner interface {
	DeleteItemPermissioner
	UpdateItemPermissioner
}

// UpdatePermissions takes in the set of permission to be added and
// removed from an item to bring it to the desired state.
func UpdatePermissions(
	ctx context.Context,
	udip updateDeleteItemPermissioner,
	driveID string,
	itemID string,
	permAdded, permRemoved []metadata.Permission,
	oldPermIDToNewID syncd.MapTo[string],
	errs *fault.Bus,
) error {
	el := errs.Local()

	// The ordering of the operations is important here. We first
	// remove all the removed permissions and then add the added ones.
	for _, p := range permRemoved {
		ictx := clues.Add(
			ctx,
			"permission_entity_type", p.EntityType,
			"permission_entity_id", clues.Hide(p.EntityID))

		// deletes require unique http clients
		// https://github.com/alcionai/corso/issues/2707
		// this is bad citizenship, and could end up consuming a lot of
		// system resources if servicers leak client connections (sockets, etc).

		pid, ok := oldPermIDToNewID.Load(p.ID)
		if !ok {
			el.AddRecoverable(ictx, clues.NewWC(ictx, "no permission matches id"))
			continue
		}

		if pid == nonRestorablePermission {
			// permission was not restored on parent and thus cannot
			// be deleted
			continue
		}

		err := udip.DeleteItemPermission(
			ictx,
			driveID,
			itemID,
			pid)
		if err != nil {
			return clues.Stack(err)
		}
	}

	for _, p := range permAdded {
		if el.Failure() != nil {
			break
		}

		ictx := clues.Add(
			ctx,
			"permission_entity_type", p.EntityType,
			"permission_entity_id", clues.Hide(p.EntityID))

		// We are not able to restore permissions when there are no
		// roles or for owner, this seems to be restriction in graph
		roles := []string{}

		for _, r := range p.Roles {
			if r != "owner" {
				roles = append(roles, r)
			}
		}

		// TODO: sitegroup support.  Currently errors with "One or more users could not be resolved",
		// likely due to the site group entityID consisting of a single integer (ex: 4)
		if len(roles) == 0 || p.EntityType == metadata.GV2SiteGroup {
			continue
		}

		pbody := drives.NewItemItemsItemInvitePostRequestBody()
		pbody.SetRoles(roles)

		if p.Expiration != nil {
			expiry := p.Expiration.String()
			pbody.SetExpirationDateTime(&expiry)
		}

		pbody.SetSendInvitation(ptr.To(false))
		pbody.SetRequireSignIn(ptr.To(true))

		rec := models.NewDriveRecipient()
		if len(p.EntityID) > 0 {
			rec.SetObjectId(&p.EntityID)
		} else {
			// Previous versions used to only store email for a
			// permissions. Use that if id is not found.
			rec.SetEmail(&p.Email)
		}

		pbody.SetRecipients([]models.DriveRecipientable{rec})

		newPerm, err := udip.PostItemPermissionUpdate(ictx, driveID, itemID, pbody)
		if graph.IsErrUsersCannotBeResolved(err) {
			oldPermIDToNewID.Store(p.ID, nonRestorablePermission)
			logger.CtxErr(ictx, err).Info("unable to restore permission")

			continue
		}

		if err != nil {
			el.AddRecoverable(ictx, clues.Stack(err))
			continue
		}

		oldPermIDToNewID.Store(p.ID, ptr.Val(newPerm.GetValue()[0].GetId()))
	}

	return el.Failure()
}

type updateDeleteItemLinkSharer interface {
	DeleteItemPermissioner // Deletion logic is same as permissions
	UpdateItemLinkSharer
}

func UpdateLinkShares(
	ctx context.Context,
	upils updateDeleteItemLinkSharer,
	driveID string,
	itemID string,
	lsAdded, lsRemoved []metadata.LinkShare,
	oldLinkShareIDToNewID syncd.MapTo[string],
	errs *fault.Bus,
) (bool, error) {
	// You can only delete inherited sharing links the first time you
	// create a sharing link which is done using
	// `retainInheritedPermissions`.  We cannot separately delete any
	// inherited link shares via DELETE API call like for permissions.
	alreadyDeleted := false
	el := errs.Local()

	for _, ls := range lsAdded {
		if el.Failure() != nil {
			break
		}

		ictx := clues.Add(ctx, "link_share_id", ls.ID)

		// Links with password are not shared with a specific user
		// even when we select a particular user, plus we are not
		// able to get the password or retain the original link and
		// so restoring them makes no sense.
		if ls.HasPassword {
			continue
		}

		idens := []map[string]string{}
		entities := []string{}

		for _, iden := range ls.Entities {
			// TODO: sitegroup support.  Currently errors with "One or more users could not be resolved",
			// likely due to the site group entityID consisting of a single integer (ex: 4)
			if iden.EntityType == metadata.GV2SiteGroup {
				continue
			}

			// Using DriveRecipient seems to error out on Graph end
			idens = append(idens, map[string]string{"objectId": iden.ID})
			entities = append(entities, iden.ID)
		}

		ictx = clues.Add(ictx, "link_share_entity_ids", strings.Join(entities, ","))

		// https://learn.microsoft.com/en-us/graph/api/driveitem-createlink?view=graph-rest-beta&tabs=http
		// v1.0 version of the graph API does not support creating a
		// link without sending a notification to the user and so we
		// use the beta API. Since we use the v1.0 API, we have to
		// stuff some of the data into the AdditionalData fields as
		// the actual fields don't exist in the stable sdk.
		// Here is the data that we have to send:
		// {
		//   "type": "view",
		//   "scope": "anonymous",
		//   "password": "String",
		//   "expirationDateTime": "...",
		//   "recipients": [{"@odata.type": "microsoft.graph.driveRecipient"}],
		//   "sendNotification": true,
		//   "retainInheritedPermissions": false
		// }
		lsbody := drives.NewItemItemsItemCreateLinkPostRequestBody()
		lsbody.SetTypeEscaped(ptr.To(ls.Link.Type))
		lsbody.SetScope(ptr.To(ls.Link.Scope))
		lsbody.SetExpirationDateTime(ls.Expiration)

		ad := map[string]any{
			"sendNotification": false,
			"recipients":       idens,
		}
		lsbody.SetAdditionalData(ad)

		if !alreadyDeleted {
			// The only way to delete any is to use this and so if
			// we have any deleted items, we can be sure that all the
			// inherited permissions would have been removed.
			lsbody.SetRetainInheritedPermissions(ptr.To(len(lsRemoved) == 0))

			// This value only effective on the first call, but lets
			// make sure to not send it on followups.
			alreadyDeleted = true
		}

		newLS, err := upils.PostItemLinkShareUpdate(ictx, driveID, itemID, lsbody)
		if graph.IsErrUsersCannotBeResolved(err) {
			oldLinkShareIDToNewID.Store(ls.ID, nonRestorablePermission)
			logger.CtxErr(ictx, err).Info("unable to restore link share")

			continue
		}

		if graph.IsErrSharingDisabled(err) {
			logger.CtxErr(ictx, err).Info("unable to restore link share; sharing disabled")
			continue
		}

		if err != nil {
			el.AddRecoverable(ictx, clues.Stack(err))
			continue
		}

		oldLinkShareIDToNewID.Store(ls.ID, ptr.Val(newLS.GetId()))
	}

	if el.Failure() != nil {
		return alreadyDeleted, el.Failure()
	}

	// It is possible to have empty link shares even though we should
	// have inherited one if the user creates a link using
	// `retainInheritedPermissions` as false, but then deleted it.  We
	// can recreate this by creating a link with no users and deleting it.
	if len(lsRemoved) > 0 && len(lsAdded) == 0 {
		lsbody := drives.NewItemItemsItemCreateLinkPostRequestBody()
		lsbody.SetTypeEscaped(ptr.To("view"))
		// creating a `users` link without any users ensure that even
		// if we fail to delete the link there are no links lying
		// around that could be used to access this
		lsbody.SetScope(ptr.To("users"))
		lsbody.SetRetainInheritedPermissions(ptr.To(false))

		newLS, err := upils.PostItemLinkShareUpdate(ctx, driveID, itemID, lsbody)
		if err != nil {
			return alreadyDeleted, clues.Stack(err)
		}

		alreadyDeleted = true

		err = upils.DeleteItemPermission(ctx, driveID, itemID, ptr.Val(newLS.GetId()))
		if err != nil {
			return alreadyDeleted, clues.Stack(err)
		}
	}

	return alreadyDeleted, nil
}

func filterUnavailableEntitiesInLinkShare(
	ctx context.Context,
	linkShares []metadata.LinkShare,
	availableEntities ResourceIDNames,
	oldLinkShareIDToNewID syncd.MapTo[string],
) []metadata.LinkShare {
	filtered := []metadata.LinkShare{}

	if availableEntities.Users == nil || availableEntities.Groups == nil {
		// This should not be happening unless we missed to fill in the caches
		logger.Ctx(ctx).Info("no available entities, not filtering link shares")
		return linkShares
	}

	for _, p := range linkShares {
		entities := []metadata.Entity{}

		for _, e := range p.Entities {
			available := false

			switch e.EntityType {
			case metadata.GV2User:
				_, ok := availableEntities.Users.NameOf(e.ID)
				available = available || ok
			case metadata.GV2Group:
				_, ok := availableEntities.Groups.NameOf(e.ID)
				available = available || ok
			default:
				// We only know about users and groups
				available = true
			}

			if available {
				entities = append(entities, e)
			}
		}

		if len(entities) > 0 {
			p.Entities = entities
			filtered = append(filtered, p)

			continue
		}

		// If we have no entities, we can't restore the link share
		// and so we have to mark it as not restored.
		// This is done to make sure we don't try to delete it later.
		oldLinkShareIDToNewID.Store(p.ID, "")
	}

	return filtered
}

func filterUnavailableEntitiesInPermissions(
	ctx context.Context,
	perms []metadata.Permission,
	availableEntities ResourceIDNames,
	oldPermIDToNewID syncd.MapTo[string],
) []metadata.Permission {
	if availableEntities.Users == nil || availableEntities.Groups == nil {
		// This should not be happening unless we missed to fill in the caches
		logger.Ctx(ctx).Info("no available entities, not filtering link shares")
		return perms
	}

	filtered := []metadata.Permission{}

	for _, p := range perms {
		available := false

		switch p.EntityType {
		case metadata.GV2User:
			_, ok := availableEntities.Users.NameOf(p.EntityID)
			available = available || ok
		case metadata.GV2Group:
			_, ok := availableEntities.Groups.NameOf(p.EntityID)
			available = available || ok
		default:
			// We only know about users and groups
			// TODO: extend the check to other entity types
			available = true
		}

		if available {
			filtered = append(filtered, p)
			continue
		}

		// If we have no entities, we can't restore the permission
		// and so we have to mark it as not restored.
		oldPermIDToNewID.Store(p.ID, "")
	}

	return filtered
}

// RestorePermissions takes in the permissions of an item, computes
// what permissions need to added and removed based on the parent
// folder metas and uses that to add/remove the necessary permissions
// on onedrive items.
func RestorePermissions(
	ctx context.Context,
	rh RestoreHandler,
	driveID string,
	itemID string,
	itemPath path.Path,
	current metadata.Metadata,
	caches *restoreCaches,
	errs *fault.Bus,
) {
	if current.SharingMode == metadata.SharingModeInherited {
		return
	}

	var didReset bool

	ctx = clues.Add(ctx, "permission_item_id", itemID)

	previousLinkShares, err := computePreviousLinkShares(ctx, itemPath, caches.ParentDirToMeta)
	if err != nil {
		errs.AddRecoverable(ctx, clues.WrapWC(ctx, err, "previous link shares"))
	}

	if previousLinkShares != nil {
		lsAdded, lsRemoved := metadata.DiffLinkShares(previousLinkShares, current.LinkShares)
		lsAdded = filterUnavailableEntitiesInLinkShare(ctx, lsAdded, caches.AvailableEntities, caches.OldLinkShareIDToNewID)

		// Link shares have to be updated before permissions as we have to
		// use the information about if we had to reset the inheritance to
		// decide if we have to restore all the permissions.
		didReset, err = UpdateLinkShares(
			ctx,
			rh,
			driveID,
			itemID,
			lsAdded,
			lsRemoved,
			caches.OldLinkShareIDToNewID,
			errs)
		if err != nil {
			errs.AddRecoverable(ctx, clues.WrapWC(ctx, err, "updating link shares"))
		}
	}

	previous, err := computePreviousMetadata(ctx, itemPath, caches.ParentDirToMeta)
	if err != nil {
		errs.AddRecoverable(ctx, clues.WrapWC(ctx, err, "previous metadata"))
		return
	}

	permAdded, permRemoved := metadata.DiffPermissions(previous.Permissions, current.Permissions)
	permAdded = filterUnavailableEntitiesInPermissions(ctx, permAdded, caches.AvailableEntities, caches.OldPermIDToNewID)

	if didReset {
		// In case we did a reset of permissions when restoring link
		// shares, we have to make sure to restore all the permissions
		// that an item has as they too will be removed.
		logger.Ctx(ctx).Debug("link share creation reset all inherited permissions")

		permRemoved = []metadata.Permission{}
		permAdded = current.Permissions
	}

	err = UpdatePermissions(
		ctx,
		rh,
		driveID,
		itemID,
		permAdded,
		permRemoved,
		caches.OldPermIDToNewID,
		errs)
	if graph.IsErrSharingDisabled(err) {
		logger.CtxErr(ctx, err).Info("sharing disabled, not restoring permissions")
		return
	}

	if err != nil {
		errs.AddRecoverable(ctx, clues.WrapWC(ctx, err, "updating permissions"))
	}
}
