package test_test

import (
	"context"
	"testing"

	"github.com/alcionai/clues"
	"github.com/google/uuid"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/alcionai/corso/src/internal/common/ptr"
	evmock "github.com/alcionai/corso/src/internal/events/mock"
	"github.com/alcionai/corso/src/internal/m365/graph"
	"github.com/alcionai/corso/src/internal/m365/onedrive"
	"github.com/alcionai/corso/src/internal/m365/resource"
	"github.com/alcionai/corso/src/internal/m365/sharepoint"
	"github.com/alcionai/corso/src/internal/tester"
	"github.com/alcionai/corso/src/internal/tester/tconfig"
	"github.com/alcionai/corso/src/internal/version"
	deeTD "github.com/alcionai/corso/src/pkg/backup/details/testdata"
	"github.com/alcionai/corso/src/pkg/control"
	ctrlTD "github.com/alcionai/corso/src/pkg/control/testdata"
	"github.com/alcionai/corso/src/pkg/count"
	"github.com/alcionai/corso/src/pkg/path"
	"github.com/alcionai/corso/src/pkg/selectors"
	selTD "github.com/alcionai/corso/src/pkg/selectors/testdata"
	"github.com/alcionai/corso/src/pkg/services/m365/api"
	storeTD "github.com/alcionai/corso/src/pkg/storage/testdata"
)

type SharePointBackupIntgSuite struct {
	tester.Suite
	its intgTesterSetup
}

func TestSharePointBackupIntgSuite(t *testing.T) {
	suite.Run(t, &SharePointBackupIntgSuite{
		Suite: tester.NewIntegrationSuite(
			t,
			[][]string{tconfig.M365AcctCredEnvs, storeTD.AWSStorageCredEnvs}),
	})
}

func (suite *SharePointBackupIntgSuite) SetupSuite() {
	suite.its = newIntegrationTesterSetup(suite.T())
}

func (suite *SharePointBackupIntgSuite) TestBackup_Run_incrementalSharePoint() {
	sel := selectors.NewSharePointRestore([]string{suite.its.siteID})

	ic := func(cs []string) selectors.Selector {
		sel.Include(sel.LibraryFolders(cs, selectors.PrefixMatch()))
		return sel.Selector
	}

	gtdi := func(
		t *testing.T,
		ctx context.Context,
	) string {
		d, err := suite.its.ac.Sites().GetDefaultDrive(ctx, suite.its.siteID)
		if err != nil {
			err = graph.Wrap(ctx, err, "retrieving default site drive").
				With("site", suite.its.siteID)
		}

		require.NoError(t, err, clues.ToCore(err))

		id := ptr.Val(d.GetId())
		require.NotEmpty(t, id, "drive ID")

		return id
	}

	grh := func(ac api.Client) onedrive.RestoreHandler {
		return sharepoint.NewRestoreHandler(ac)
	}

	runDriveIncrementalTest(
		suite,
		suite.its.siteID,
		suite.its.userID,
		resource.Sites,
		path.SharePointService,
		path.LibrariesCategory,
		ic,
		gtdi,
		grh,
		true)
}

func (suite *SharePointBackupIntgSuite) TestBackup_Run_sharePoint() {
	t := suite.T()

	ctx, flush := tester.NewContext(t)
	defer flush()

	var (
		mb   = evmock.NewBus()
		sel  = selectors.NewSharePointBackup([]string{suite.its.siteID})
		opts = control.Defaults()
	)

	sel.Include(selTD.SharePointBackupFolderScope(sel))

	bo, bod := prepNewTestBackupOp(t, ctx, mb, sel.Selector, opts, version.Backup)
	defer bod.close(t, ctx)

	runAndCheckBackup(t, ctx, &bo, mb, false)
	checkBackupIsInManifests(
		t,
		ctx,
		bod.kw,
		bod.sw,
		&bo,
		bod.sel,
		suite.its.siteID,
		path.LibrariesCategory)
}

func (suite *SharePointBackupIntgSuite) TestBackup_Run_sharePointExtensions() {
	t := suite.T()

	ctx, flush := tester.NewContext(t)
	defer flush()

	var (
		mb    = evmock.NewBus()
		sel   = selectors.NewSharePointBackup([]string{suite.its.siteID})
		opts  = control.Defaults()
		tenID = tconfig.M365TenantID(t)
		svc   = path.SharePointService
		ws    = deeTD.DriveIDFromRepoRef
	)

	opts.ItemExtensionFactory = getTestExtensionFactories()

	sel.Include(selTD.SharePointBackupFolderScope(sel))

	bo, bod := prepNewTestBackupOp(t, ctx, mb, sel.Selector, opts, version.Backup)
	defer bod.close(t, ctx)

	runAndCheckBackup(t, ctx, &bo, mb, false)
	checkBackupIsInManifests(
		t,
		ctx,
		bod.kw,
		bod.sw,
		&bo,
		bod.sel,
		suite.its.siteID,
		path.LibrariesCategory)

	bID := bo.Results.BackupID

	deets, expectDeets := deeTD.GetDeetsInBackup(
		t,
		ctx,
		bID,
		tenID,
		bod.sel.ID(),
		svc,
		ws,
		bod.kms,
		bod.sss)
	deeTD.CheckBackupDetails(
		t,
		ctx,
		bID,
		ws,
		bod.kms,
		bod.sss,
		expectDeets,
		false)

	// Check that the extensions are in the backup
	for _, ent := range deets.Entries {
		if ent.Folder == nil {
			verifyExtensionData(t, ent.ItemInfo, path.SharePointService)
		}
	}
}

type SharePointRestoreIntgSuite struct {
	tester.Suite
	its intgTesterSetup
}

func TestSharePointRestoreIntgSuite(t *testing.T) {
	suite.Run(t, &SharePointRestoreIntgSuite{
		Suite: tester.NewIntegrationSuite(
			t,
			[][]string{tconfig.M365AcctCredEnvs, storeTD.AWSStorageCredEnvs}),
	})
}

func (suite *SharePointRestoreIntgSuite) SetupSuite() {
	suite.its = newIntegrationTesterSetup(suite.T())
}

func (suite *SharePointRestoreIntgSuite) TestRestore_Run_sharepointWithAdvancedOptions() {
	sel := selectors.NewSharePointBackup([]string{suite.its.siteID})
	sel.Include(selTD.SharePointBackupFolderScope(sel))
	sel.Filter(sel.Library("documents"))
	sel.DiscreteOwner = suite.its.siteID

	runDriveRestoreWithAdvancedOptions(
		suite.T(),
		suite,
		suite.its.ac,
		sel.Selector,
		suite.its.siteDriveID,
		suite.its.siteDriveRootFolderID)
}

func (suite *SharePointRestoreIntgSuite) TestRestore_Run_sharepointDeletedDrives() {
	t := suite.T()

	// despite the client having a method for drive.Patch and drive.Delete, both only return
	// the error code and message `invalidRequest`.
	t.Skip("graph api doesn't allow patch or delete on drives, so we cannot run any conditions")

	ctx, flush := tester.NewContext(t)
	defer flush()

	rc := ctrlTD.DefaultRestoreConfig("restore_deleted_drives")
	rc.OnCollision = control.Copy

	// create a new drive
	md, err := suite.its.ac.Lists().PostDrive(ctx, suite.its.siteID, rc.Location)
	require.NoError(t, err, clues.ToCore(err))

	driveID := ptr.Val(md.GetId())

	// get the root folder
	mdi, err := suite.its.ac.Drives().GetRootFolder(ctx, driveID)
	require.NoError(t, err, clues.ToCore(err))

	rootFolderID := ptr.Val(mdi.GetId())

	// add an item to it
	itemName := uuid.NewString()

	item := models.NewDriveItem()
	item.SetName(ptr.To(itemName + ".txt"))

	file := models.NewFile()
	item.SetFile(file)

	_, err = suite.its.ac.Drives().PostItemInContainer(
		ctx,
		driveID,
		rootFolderID,
		item,
		control.Copy)
	require.NoError(t, err, clues.ToCore(err))

	// run a backup
	var (
		mb          = evmock.NewBus()
		opts        = control.Defaults()
		graphClient = suite.its.ac.Stable.Client()
	)

	bsel := selectors.NewSharePointBackup([]string{suite.its.siteID})
	bsel.Include(selTD.SharePointBackupFolderScope(bsel))
	bsel.Filter(bsel.Library(rc.Location))
	bsel.DiscreteOwner = suite.its.siteID

	bo, bod := prepNewTestBackupOp(t, ctx, mb, bsel.Selector, opts, version.Backup)
	defer bod.close(t, ctx)

	runAndCheckBackup(t, ctx, &bo, mb, false)

	// test cases:

	// first test, we take the current drive and rename it.
	// the restore should find the drive by id and restore items
	// into it like normal.  Due to collision handling, this should
	// create a copy of the current item.
	suite.Run("renamed drive", func() {
		t := suite.T()

		ctx, flush := tester.NewContext(t)
		defer flush()

		patchBody := models.NewDrive()
		patchBody.SetName(ptr.To("some other name"))

		md, err = graphClient.
			Drives().
			ByDriveId(driveID).
			Patch(ctx, patchBody, nil)
		require.NoError(t, err, clues.ToCore(graph.Stack(ctx, err)))

		var (
			mb  = evmock.NewBus()
			ctr = count.New()
		)

		ro, _ := prepNewTestRestoreOp(
			t,
			ctx,
			bod.st,
			bo.Results.BackupID,
			mb,
			ctr,
			bod.sel,
			opts,
			rc)

		runAndCheckRestore(t, ctx, &ro, mb, false)
		assert.Equal(t, 1, ctr.Get(count.NewItemCreated), "restored an item")

		resp, err := graphClient.
			Drives().
			ByDriveId(driveID).
			Items().
			ByDriveItemId(rootFolderID).
			Children().
			Get(ctx, nil)
		require.NoError(t, err, clues.ToCore(graph.Stack(ctx, err)))

		items := resp.GetValue()
		assert.Len(t, items, 2)

		for _, item := range items {
			assert.Contains(t, ptr.Val(item.GetName()), itemName)
		}
	})

	// second test, we delete the drive altogether.  the restore should find
	// no existing drives, but it should have the old drive's name and attempt
	// to recreate that drive by name.
	suite.Run("deleted drive", func() {
		t := suite.T()

		ctx, flush := tester.NewContext(t)
		defer flush()

		err = graphClient.
			Drives().
			ByDriveId(driveID).
			Delete(ctx, nil)
		require.NoError(t, err, clues.ToCore(graph.Stack(ctx, err)))

		var (
			mb  = evmock.NewBus()
			ctr = count.New()
		)

		ro, _ := prepNewTestRestoreOp(
			t,
			ctx,
			bod.st,
			bo.Results.BackupID,
			mb,
			ctr,
			bod.sel,
			opts,
			rc)

		runAndCheckRestore(t, ctx, &ro, mb, false)
		assert.Equal(t, 1, ctr.Get(count.NewItemCreated), "restored an item")

		pgr := suite.its.ac.
			Drives().
			NewSiteDrivePager(suite.its.siteID, []string{"id", "name"})

		drives, err := api.GetAllDrives(ctx, pgr, false, -1)
		require.NoError(t, err, clues.ToCore(err))

		var created models.Driveable

		for _, drive := range drives {
			if ptr.Val(drive.GetName()) == ptr.Val(created.GetName()) &&
				ptr.Val(drive.GetId()) != driveID {
				created = drive
				break
			}
		}

		require.NotNil(t, created, "found the restored drive by name")
		md = created
		driveID = ptr.Val(md.GetId())

		mdi, err := suite.its.ac.Drives().GetRootFolder(ctx, driveID)
		require.NoError(t, err, clues.ToCore(err))

		rootFolderID = ptr.Val(mdi.GetId())

		resp, err := graphClient.
			Drives().
			ByDriveId(driveID).
			Items().
			ByDriveItemId(rootFolderID).
			Children().
			Get(ctx, nil)
		require.NoError(t, err, clues.ToCore(graph.Stack(ctx, err)))

		items := resp.GetValue()
		assert.Len(t, items, 1)

		assert.Equal(t, ptr.Val(items[0].GetName()), itemName+".txt")
	})

	// final test, run a follow-up restore.  This should match the
	// drive we created in the prior test by name, but not by ID.
	suite.Run("different drive - same name", func() {
		t := suite.T()

		ctx, flush := tester.NewContext(t)
		defer flush()

		var (
			mb  = evmock.NewBus()
			ctr = count.New()
		)

		ro, _ := prepNewTestRestoreOp(
			t,
			ctx,
			bod.st,
			bo.Results.BackupID,
			mb,
			ctr,
			bod.sel,
			opts,
			rc)

		runAndCheckRestore(t, ctx, &ro, mb, false)

		assert.Equal(t, 1, ctr.Get(count.NewItemCreated), "restored an item")

		resp, err := graphClient.
			Drives().
			ByDriveId(driveID).
			Items().
			ByDriveItemId(rootFolderID).
			Children().
			Get(ctx, nil)
		require.NoError(t, err, clues.ToCore(graph.Stack(ctx, err)))

		items := resp.GetValue()
		assert.Len(t, items, 2)

		for _, item := range items {
			assert.Contains(t, ptr.Val(item.GetName()), itemName)
		}
	})
}