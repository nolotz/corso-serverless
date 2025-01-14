package drive

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alcionai/clues"
	"github.com/pkg/errors"
	"github.com/spatialcurrent/go-lazy/pkg/lazy"

	"github.com/alcionai/corso/src/internal/common/idname"
	"github.com/alcionai/corso/src/internal/common/ptr"
	"github.com/alcionai/corso/src/internal/data"
	"github.com/alcionai/corso/src/internal/m365/collection/drive/metadata"
	"github.com/alcionai/corso/src/internal/m365/support"
	"github.com/alcionai/corso/src/internal/observe"
	"github.com/alcionai/corso/src/pkg/backup/details"
	"github.com/alcionai/corso/src/pkg/control"
	"github.com/alcionai/corso/src/pkg/count"
	"github.com/alcionai/corso/src/pkg/errs/core"
	"github.com/alcionai/corso/src/pkg/extensions"
	"github.com/alcionai/corso/src/pkg/fault"
	"github.com/alcionai/corso/src/pkg/logger"
	"github.com/alcionai/corso/src/pkg/path"
	"github.com/alcionai/corso/src/pkg/services/m365/api"
	"github.com/alcionai/corso/src/pkg/services/m365/api/graph"
	"github.com/alcionai/corso/src/pkg/services/m365/custom"
)

const (
	// Used to compare in case of OneNote files
	MaxOneNoteFileSize = 2 * 1024 * 1024 * 1024

	oneNoteMimeType = "application/msonenote"
)

var _ data.BackupCollection = &Collection{}

// Collection represents a set of OneDrive objects retrieved from M365
type Collection struct {
	handler BackupHandler

	// the protected resource represented in this collection.
	protectedResource idname.Provider

	// data is used to share data streams with the collection consumer
	data chan data.Item
	// folderPath indicates what level in the hierarchy this collection
	// represents
	folderPath path.Path
	// M365 IDs of file items within this collection
	driveItems map[string]*custom.DriveItem

	// Primary M365 ID of the drive this collection was created from
	driveID   string
	driveName string

	statusUpdater support.StatusUpdater
	ctrl          control.Options

	// PrevPath is the previous hierarchical path used by this collection.
	// It may be the same as fullPath, if the folder was not renamed or
	// moved.  It will be empty on its first retrieval.
	prevPath path.Path

	// locPath represents the human-readable location of this collection.
	locPath *path.Builder
	// prevLocPath represents the human-readable location of this collection in
	// the previous backup.
	prevLocPath *path.Builder

	// Specifies if it new, moved/rename or deleted
	state data.CollectionState

	// true if this collection, or a parent directory of this collection,
	// is marked as a package.
	// packages are only marked on the top-level directory, but special-case
	// handling need apply to all subfolders.  Therefore it is necessary to cascade
	// that identification to all affected collections, not just those that identify
	// as packages themselves.
	// see: https://learn.microsoft.com/en-us/graph/api/resources/package?view=graph-rest-1.0
	isPackageOrChildOfPackage bool

	// should only be true if the old delta token expired
	doNotMergeItems bool

	urlCache getItemPropertyer

	counter *count.Bus
}

func pathToLocation(p path.Path) (*path.Builder, error) {
	if p == nil {
		return nil, nil
	}

	dp, err := path.ToDrivePath(p)
	if err != nil {
		return nil, err
	}

	return path.Builder{}.Append(dp.Root).Append(dp.Folders...), nil
}

// NewCollection creates a Collection
func NewCollection(
	handler BackupHandler,
	resource idname.Provider,
	currPath path.Path,
	prevPath path.Path,
	driveID, driveName string,
	statusUpdater support.StatusUpdater,
	ctrlOpts control.Options,
	isPackageOrChildOfPackage bool,
	doNotMergeItems bool,
	urlCache getItemPropertyer,
	counter *count.Bus,
) (*Collection, error) {
	// TODO(ashmrtn): If OneDrive switches to using folder IDs then this will need
	// to be changed as we won't be able to extract path information from the
	// storage path. In that case, we'll need to start storing the location paths
	// like we do the previous path.
	locPath, err := pathToLocation(currPath)
	if err != nil {
		return nil, clues.Wrap(err, "getting location").With("curr_path", currPath.String())
	}

	prevLocPath, err := pathToLocation(prevPath)
	if err != nil {
		return nil, clues.Wrap(err, "getting previous location").With("prev_path", prevPath.String())
	}

	c := newColl(
		handler,
		resource,
		currPath,
		prevPath,
		driveID,
		driveName,
		statusUpdater,
		ctrlOpts,
		isPackageOrChildOfPackage,
		doNotMergeItems,
		urlCache,
		counter)

	c.locPath = locPath
	c.prevLocPath = prevLocPath

	return c, nil
}

func newColl(
	handler BackupHandler,
	resource idname.Provider,
	currPath path.Path,
	prevPath path.Path,
	driveID, driveName string,
	statusUpdater support.StatusUpdater,
	ctrlOpts control.Options,
	isPackageOrChildOfPackage bool,
	doNotMergeItems bool,
	urlCache getItemPropertyer,
	counter *count.Bus,
) *Collection {
	dataCh := make(chan data.Item, graph.Parallelism(path.OneDriveMetadataService).CollectionBufferSize())

	c := &Collection{
		handler:                   handler,
		protectedResource:         resource,
		folderPath:                currPath,
		prevPath:                  prevPath,
		driveItems:                map[string]*custom.DriveItem{},
		driveID:                   driveID,
		driveName:                 driveName,
		data:                      dataCh,
		statusUpdater:             statusUpdater,
		ctrl:                      ctrlOpts,
		state:                     data.StateOf(prevPath, currPath, counter),
		isPackageOrChildOfPackage: isPackageOrChildOfPackage,
		doNotMergeItems:           doNotMergeItems,
		urlCache:                  urlCache,
		counter:                   counter,
	}

	return c
}

// Adds an itemID to the collection.  This will make it eligible to be
// populated. The return values denotes if the item was previously
// present or is new one.
func (oc *Collection) Add(item *custom.DriveItem) bool {
	_, found := oc.driveItems[ptr.Val(item.GetId())]
	oc.driveItems[ptr.Val(item.GetId())] = item

	// if !found, it's a new addition
	return !found
}

// Remove removes a item from the collection
func (oc *Collection) Remove(itemID string) bool {
	_, found := oc.driveItems[itemID]
	if !found {
		return false
	}

	delete(oc.driveItems, itemID)

	return true
}

// IsEmpty check if a collection does not contain any items
func (oc *Collection) IsEmpty() bool {
	return len(oc.driveItems) == 0
}

// ContainsItem returns true if the collection has the given item as one of its
// children.
func (oc Collection) ContainsItem(item *custom.DriveItem) bool {
	_, ok := oc.driveItems[ptr.Val(item.GetId())]
	return ok
}

// AddedItems returns the number of non-deleted items in the collection.
func (oc Collection) CountAddedItems() int {
	// Subtract one since the folder is added to the collection so we get folder
	// metadata. The collection of the root folder of the drive doesn't have its
	// own folder reference since it doesn't have permissions the user can change,
	// but it's close enough for our purposes.
	return len(oc.driveItems) - 1
}

// Items() returns the channel containing M365 Exchange objects
func (oc *Collection) Items(
	ctx context.Context,
	errs *fault.Bus,
) <-chan data.Item {
	go oc.streamItems(ctx, errs)
	return oc.data
}

func (oc *Collection) FullPath() path.Path {
	return oc.folderPath
}

func (oc Collection) PreviousPath() path.Path {
	return oc.prevPath
}

func (oc *Collection) SetFullPath(curPath path.Path) {
	oc.folderPath = curPath
	oc.state = data.StateOf(oc.prevPath, curPath, oc.counter)
}

func (oc Collection) LocationPath() *path.Builder {
	return oc.locPath
}

func (oc Collection) PreviousLocationPath() details.LocationIDer {
	if oc.prevLocPath == nil {
		return nil
	}

	return oc.handler.NewLocationIDer(oc.driveID, oc.prevLocPath.Elements()...)
}

func (oc Collection) State() data.CollectionState {
	return oc.state
}

func (oc Collection) DoNotMergeItems() bool {
	return oc.doNotMergeItems
}

// getDriveItemContent fetch drive item's contents with retries
func (oc *Collection) getDriveItemContent(
	ctx context.Context,
	driveID string,
	item *custom.DriveItem,
	errs *fault.Bus,
) (io.ReadCloser, error) {
	var (
		itemID   = ptr.Val(item.GetId())
		itemName = ptr.Val(item.GetName())
	)

	itemData, err := downloadContent(
		ctx,
		oc.handler,
		oc.urlCache,
		item,
		oc.driveID,
		oc.counter)
	if err != nil {
		if clues.HasLabel(err, graph.LabelsMalware) || (item != nil && item.GetMalware() != nil) {
			logger.CtxErr(ctx, err).With("skipped_reason", fault.SkipMalware).Info("item flagged as malware")
			errs.AddSkip(ctx, fault.FileSkip(fault.SkipMalware, driveID, itemID, itemName, graph.ItemInfo(item)))

			return nil, clues.Wrap(err, "malware item").Label(graph.LabelsSkippable)
		}

		if clues.HasLabel(err, graph.LabelStatus(http.StatusNotFound)) || errors.Is(err, core.ErrNotFound) {
			logger.CtxErr(ctx, err).Info("item not found, probably deleted in flight")
			return nil, clues.Wrap(err, "deleted item").Label(graph.LabelsSkippable)
		}

		var itemMimeType string
		if item.GetFile() != nil {
			itemMimeType = ptr.Val(item.GetFile().GetMimeType())
		}
		// Skip big OneNote files as they can't be downloaded
		if clues.HasLabel(err, graph.LabelStatus(http.StatusServiceUnavailable)) &&
			// oc.isPackageOrChildOfPackage && *item.GetSize() >= MaxOneNoteFileSize {
			// TODO: We've removed the file size check because it looks like we've seen persistent
			// 503's with smaller OneNote files also.
			oc.isPackageOrChildOfPackage || strings.EqualFold(itemMimeType, oneNoteMimeType) {
			// FIXME: It is possible that in case of a OneNote file we
			// will end up just backing up the `onetoc2` file without
			// the one file which is the important part of the OneNote
			// "item". This will have to be handled during the
			// restore, or we have to handle it separately by somehow
			// deleting the entire collection.
			logger.
				CtxErr(ctx, err).
				With("skipped_reason", fault.SkipOneNote).
				Info("inaccessible one note file")
			errs.AddSkip(ctx, fault.FileSkip(
				fault.SkipOneNote,
				driveID,
				itemID,
				itemName,
				graph.ItemInfo(item)))

			return nil, clues.Wrap(err, "inaccesible oneNote item").Label(graph.LabelsSkippable)
		}

		// return err, not el.Err(), because the lazy reader needs to communicate to
		// the data consumer that this item is unreadable, regardless of the fault state.
		return nil, clues.Wrap(err, "downloading item content")
	}

	return itemData, nil
}

type itemAndAPIGetter interface {
	GetItemer
	api.Getter
}

// downloadContent attempts to fetch the item content.  If the content url
// is expired (ie, returns a 401), it re-fetches the item to get a new download
// url and tries again.
func downloadContent(
	ctx context.Context,
	iaag itemAndAPIGetter,
	uc getItemPropertyer,
	item *custom.DriveItem,
	driveID string,
	counter *count.Bus,
) (io.ReadCloser, error) {
	itemID := ptr.Val(item.GetId())
	ctx = clues.Add(ctx, "item_id", itemID)

	content, err := downloadItem(ctx, iaag, item)
	if err == nil {
		return content, nil
	} else if !graph.IsErrUnauthorizedOrBadToken(err) {
		return nil, err
	}

	// Assume unauthorized requests are a sign of an expired jwt
	// token, and that we've overrun the available window to
	// download the file.  Get a fresh url from the cache and attempt to
	// download again.
	content, err = readItemContents(ctx, iaag, uc, itemID)
	if err == nil {
		logger.Ctx(ctx).Debug("found item in url cache")
		return content, nil
	}

	// Consider cache errors(including deleted items) as cache misses.
	// Fallback to refetching the item using the graph API.
	logger.CtxErr(ctx, err).Debug("url cache miss: refetching from API")
	counter.Inc(count.URLCacheMiss)

	di, err := iaag.GetItem(ctx, driveID, ptr.Val(item.GetId()))
	if err != nil {
		return nil, clues.Wrap(err, "retrieving expired item")
	}

	cdi := custom.ToCustomDriveItem(di)

	content, err = downloadItem(ctx, iaag, cdi)
	if err != nil {
		return nil, clues.Wrap(err, "content download retry")
	}

	return content, nil
}

// readItemContents fetches latest download URL from the cache and attempts to
// download the file using the new URL.
func readItemContents(
	ctx context.Context,
	iaag itemAndAPIGetter,
	uc getItemPropertyer,
	itemID string,
) (io.ReadCloser, error) {
	if uc == nil {
		return nil, clues.New("nil url cache")
	}

	props, err := uc.getItemProperties(ctx, itemID)
	if err != nil {
		return nil, err
	}

	// Handle newly deleted items
	if props.isDeleted {
		logger.Ctx(ctx).Info("item deleted in cache")
		return nil, core.ErrNotFound
	}

	rc, err := downloadFile(ctx, iaag, props.downloadURL)
	if graph.IsErrUnauthorizedOrBadToken(err) {
		logger.CtxErr(ctx, err).Debug("stale item in cache")
	}

	if err != nil {
		return nil, err
	}

	return rc, nil
}

type driveStats struct {
	dirsRead   int64
	dirsFound  int64
	byteCount  int64
	itemsRead  int64
	itemsFound int64
}

// streamItems iterates through items added to the collection
// and uses the collection `itemReader` to read the item
func (oc *Collection) streamItems(ctx context.Context, errs *fault.Bus) {
	var (
		stats driveStats
		wg    sync.WaitGroup
	)

	// Retrieve the OneDrive folder path to set later in
	// `details.OneDriveInfo`
	parentPath, err := path.GetDriveFolderPath(oc.folderPath)
	if err != nil {
		logger.CtxErr(ctx, err).Info("getting drive folder path")
		oc.reportAsCompleted(ctx, 0, 0, 0)

		return
	}

	progressMessage := observe.ProgressWithCount(
		ctx,
		observe.ItemQueueMsg,
		path.NewElements(oc.handler.FormatDisplayPath(oc.driveName, parentPath)),
		int64(len(oc.driveItems)))
	defer close(progressMessage)

	semaphoreCh := make(chan struct{}, graph.Parallelism(path.OneDriveService).Item())
	defer close(semaphoreCh)

	ctx = clues.Add(ctx,
		"parent_path", parentPath,
		"is_package", oc.isPackageOrChildOfPackage)

	for _, item := range oc.driveItems {
		if errs.Failure() != nil {
			break
		}

		semaphoreCh <- struct{}{}

		wg.Add(1)

		go func(item *custom.DriveItem) {
			defer wg.Done()
			defer func() { <-semaphoreCh }()

			// Read the item
			oc.streamDriveItem(
				ctx,
				parentPath,
				item,
				&stats,
				oc.ctrl.ItemExtensionFactory,
				errs)

			progressMessage <- struct{}{}
		}(item)
	}

	wg.Wait()

	oc.reportAsCompleted(ctx, int(stats.itemsFound), int(stats.itemsRead), stats.byteCount)
}

type lazyItemGetter struct {
	info                 *details.ItemInfo
	item                 *custom.DriveItem
	driveID              string
	suffix               string
	itemExtensionFactory []extensions.CreateItemExtensioner
	contentGetter        func(
		ctx context.Context,
		driveID string,
		item *custom.DriveItem,
		errs *fault.Bus) (io.ReadCloser, error)
}

func (lig *lazyItemGetter) GetData(
	ctx context.Context,
	errs *fault.Bus,
) (io.ReadCloser, *details.ItemInfo, bool, error) {
	rc, err := lig.contentGetter(ctx, lig.driveID, lig.item, errs)
	if err != nil {
		return nil, nil, false, clues.Stack(err)
	}

	extRc, extData, err := extensions.AddItemExtensions(
		ctx,
		rc,
		*lig.info,
		lig.itemExtensionFactory)
	if err != nil {
		err := clues.WrapWC(ctx, err, "adding extensions").
			Label(fault.LabelForceNoBackupCreation)

		return nil, nil, false, err
	}

	lig.info.Extension.Data = extData.Data

	// display/log the item download
	progReader := observe.ItemProgress(
		ctx,
		extRc,
		observe.ItemBackupMsg,
		clues.Hide(ptr.Val(lig.item.GetName())+lig.suffix),
		ptr.Val(lig.item.GetSize()))

	return progReader, lig.info, false, nil
}

func (oc *Collection) streamDriveItem(
	ctx context.Context,
	parentPath *path.Builder,
	item *custom.DriveItem,
	stats *driveStats,
	itemExtensionFactory []extensions.CreateItemExtensioner,
	errs *fault.Bus,
) {
	var (
		itemID       = ptr.Val(item.GetId())
		itemName     = ptr.Val(item.GetName())
		itemSize     = ptr.Val(item.GetSize())
		itemInfo     details.ItemInfo
		itemMeta     io.ReadCloser
		itemMetaSize int
		metaFileName string
		metaSuffix   string
		err          error
	)

	ctx = clues.Add(
		ctx,
		"item_id", itemID,
		"item_name", clues.Hide(itemName),
		"item_size", itemSize)

	item.SetParentReference(custom.SetParentName(item.GetParentReference(), oc.driveName))

	isFile := item.GetFile() != nil

	if isFile {
		atomic.AddInt64(&stats.itemsFound, 1)

		if oc.counter.Inc(count.StreamItemsFound)%1000 == 0 {
			logger.Ctx(ctx).Infow("item stream progress", "stats", oc.counter.Values())
		}

		metaFileName = itemID
		metaSuffix = metadata.MetaFileSuffix
	} else {
		atomic.AddInt64(&stats.dirsFound, 1)
		oc.counter.Inc(count.StreamDirsFound)

		// metaFileName not set for directories so we get just ".dirmeta"
		metaSuffix = metadata.DirMetaFileSuffix
	}

	// Fetch metadata for the item
	itemMeta, itemMetaSize, err = downloadItemMeta(ctx, oc.handler, oc.driveID, item)
	if err != nil {
		// Skip deleted items
		if !clues.HasLabel(err, graph.LabelStatus(http.StatusNotFound)) && !errors.Is(err, core.ErrNotFound) {
			errs.AddRecoverable(ctx, clues.Wrap(err, "getting item metadata").Label(fault.LabelForceNoBackupCreation))
		}

		return
	}

	itemInfo = oc.handler.AugmentItemInfo(
		itemInfo,
		oc.protectedResource,
		item,
		itemSize,
		parentPath)

	ctx = clues.Add(ctx, "item_info", itemInfo)

	// Drive content download requests are also rate limited by graph api.
	// Ensure that this request goes through the drive limiter & not the default
	// limiter.
	ctx = graph.BindRateLimiterConfig(
		ctx,
		graph.LimiterCfg{
			Service: path.OneDriveService,
		})

	if isFile {
		dataSuffix := metadata.DataFileSuffix

		// Use a LazyItem to feed to the collection consumer.
		// This ensures that downloads won't be attempted unless that consumer
		// attempts to read bytes.  Assumption is that kopia will check things
		// like file modtimes before attempting to read.
		oc.data <- data.NewLazyItemWithInfo(
			ctx,
			&lazyItemGetter{
				info:                 &itemInfo,
				item:                 item,
				driveID:              oc.driveID,
				itemExtensionFactory: itemExtensionFactory,
				contentGetter:        oc.getDriveItemContent,
				suffix:               dataSuffix,
			},
			itemID+dataSuffix,
			itemInfo.Modified(),
			oc.counter,
			errs)
	}

	metaReader := lazy.NewLazyReadCloser(func() (io.ReadCloser, error) {
		progReader := observe.ItemProgress(
			ctx,
			itemMeta,
			observe.ItemBackupMsg,
			clues.Hide(itemName+metaSuffix),
			int64(itemMetaSize))
		return progReader, nil
	})

	storeItem, err := data.NewPrefetchedItem(
		metaReader,
		metaFileName+metaSuffix,
		// Metadata file should always use the latest time as
		// permissions change does not update mod time.
		time.Now())
	if err != nil {
		errs.AddRecoverable(ctx, clues.StackWC(ctx, err).
			Label(fault.LabelForceNoBackupCreation))

		return
	}

	// We wrap the reader with a lazy reader so that the progress bar is only
	// initialized if the file is read. Since we're not actually lazily reading
	// data just use the eager item implementation.
	oc.data <- storeItem

	// Item read successfully, add to collection
	if isFile {
		oc.counter.Inc(count.StreamItemsAdded)
		atomic.AddInt64(&stats.itemsRead, 1)
	} else {
		oc.counter.Inc(count.StreamDirsAdded)
		atomic.AddInt64(&stats.dirsRead, 1)
	}

	oc.counter.Add(count.StreamBytesAdded, itemSize)
	atomic.AddInt64(&stats.byteCount, itemSize)
}

func (oc *Collection) reportAsCompleted(ctx context.Context, itemsFound, itemsRead int, byteCount int64) {
	close(oc.data)

	status := support.CreateStatus(ctx, support.Backup,
		1, // num folders (always 1)
		support.CollectionMetrics{
			Objects:   itemsFound,
			Successes: itemsRead,
			Bytes:     byteCount,
		},
		oc.folderPath.Folder(false))

	logger.Ctx(ctx).Debugw("done streaming items", "status", status.String())

	oc.statusUpdater(status)
}
