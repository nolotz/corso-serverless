package groups

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alcionai/clues"
	kjson "github.com/microsoft/kiota-serialization-json-go"

	"github.com/alcionai/corso/src/internal/data"
	"github.com/alcionai/corso/src/internal/m365/support"
	"github.com/alcionai/corso/src/internal/observe"
	"github.com/alcionai/corso/src/pkg/backup/details"
	"github.com/alcionai/corso/src/pkg/count"
	"github.com/alcionai/corso/src/pkg/errs/core"
	"github.com/alcionai/corso/src/pkg/fault"
	"github.com/alcionai/corso/src/pkg/logger"
	"github.com/alcionai/corso/src/pkg/path"
	"github.com/alcionai/corso/src/pkg/services/m365/api/graph"
)

var (
	_ data.BackupCollection = &prefetchCollection[graph.GetIDer, groupsItemer]{}
	_ data.BackupCollection = &lazyFetchCollection[graph.GetIDer, groupsItemer]{}
)

const (
	collectionChannelBufferSize = 1000
	numberOfRetries             = 4
)

// updateStatus is a utility function used to send the status update through
// the channel.
func updateStatus(
	ctx context.Context,
	statusUpdater support.StatusUpdater,
	attempted int,
	streamedItems int64,
	totalBytes int64,
	folderPath string,
	err error,
) {
	status := support.CreateStatus(
		ctx,
		support.Backup,
		1,
		support.CollectionMetrics{
			Objects:   attempted,
			Successes: int(streamedItems),
			Bytes:     totalBytes,
		},
		folderPath)

	logger.Ctx(ctx).Debugw("done streaming items", "status", status.String())

	statusUpdater(status)
}

// -----------------------------------------------------------------------------
// prefetchCollection
// -----------------------------------------------------------------------------

type prefetchCollection[C graph.GetIDer, I groupsItemer] struct {
	data.BaseCollection
	protectedResource string
	stream            chan data.Item

	contains container[C]

	// added is a list of existing item IDs that were added to a container
	added map[string]time.Time
	// removed is a list of item IDs that were deleted from, or moved out, of a container
	removed map[string]struct{}

	getAndAugment getItemAndAugmentInfoer[C, I]

	statusUpdater support.StatusUpdater
}

// State of the collection is set as an observation of the current
// and previous paths.  If the curr path is nil, the state is assumed
// to be deleted.  If the prev path is nil, it is assumed newly created.
// If both are populated, then state is either moved (if they differ),
// or notMoved (if they match).
func NewCollection[C graph.GetIDer, I groupsItemer](
	baseCol data.BaseCollection,
	getAndAugment getItemAndAugmentInfoer[C, I],
	protectedResource string,
	added map[string]time.Time,
	removed map[string]struct{},
	contains container[C],
	statusUpdater support.StatusUpdater,
	useLazyReader bool,
) data.BackupCollection {
	if useLazyReader {
		return &lazyFetchCollection[C, I]{
			BaseCollection:    baseCol,
			added:             added,
			contains:          contains,
			getAndAugment:     getAndAugment,
			removed:           removed,
			statusUpdater:     statusUpdater,
			stream:            make(chan data.Item, collectionChannelBufferSize),
			protectedResource: protectedResource,
		}
	}

	return &prefetchCollection[C, I]{
		BaseCollection:    baseCol,
		added:             added,
		contains:          contains,
		getAndAugment:     getAndAugment,
		removed:           removed,
		statusUpdater:     statusUpdater,
		stream:            make(chan data.Item, collectionChannelBufferSize),
		protectedResource: protectedResource,
	}
}

func (col *prefetchCollection[C, I]) Items(ctx context.Context, errs *fault.Bus) <-chan data.Item {
	go col.streamItems(ctx, errs)
	return col.stream
}

func (col *prefetchCollection[C, I]) streamItems(ctx context.Context, errs *fault.Bus) {
	var (
		streamedItems   int64
		totalBytes      int64
		wg              sync.WaitGroup
		progressMessage chan<- struct{}
		el              = errs.Local()
	)

	ctx = clues.Add(ctx, "category", col.Category().String())

	defer func() {
		close(col.stream)
		logger.Ctx(ctx).Infow(
			"finished stream backup collection items",
			"stats", col.Counter.Values())

		updateStatus(
			ctx,
			col.statusUpdater,
			len(col.added)+len(col.removed),
			streamedItems,
			totalBytes,
			col.FullPath().Folder(false),
			errs.Failure())
	}()

	if len(col.added)+len(col.removed) > 0 {
		progressMessage = observe.CollectionProgress(
			ctx,
			col.Category().HumanString(),
			col.LocationPath().Elements())
		defer close(progressMessage)
	}

	semaphoreCh := make(chan struct{}, col.Opts().Parallelism.ItemFetch)
	defer close(semaphoreCh)

	// delete all removed items
	for id := range col.removed {
		semaphoreCh <- struct{}{}

		wg.Add(1)

		go func(id string) {
			defer wg.Done()
			defer func() { <-semaphoreCh }()

			col.stream <- data.NewDeletedItem(id)

			atomic.AddInt64(&streamedItems, 1)
			col.Counter.Inc(count.StreamItemsRemoved)

			if progressMessage != nil {
				progressMessage <- struct{}{}
			}
		}(id)
	}

	// add any new items
	for id := range col.added {
		if el.Failure() != nil {
			break
		}

		wg.Add(1)
		semaphoreCh <- struct{}{}

		go func(id string) {
			defer wg.Done()
			defer func() { <-semaphoreCh }()

			writer := kjson.NewJsonSerializationWriter()
			defer writer.Close()

			item, info, err := col.getAndAugment.getItem(
				ctx,
				col.protectedResource,
				col.FullPath().Folders(),
				id)
			if err != nil {
				err = clues.Wrap(err, "getting channel message data").Label(fault.LabelForceNoBackupCreation)
				el.AddRecoverable(ctx, err)

				return
			}

			col.getAndAugment.augmentItemInfo(info, col.contains.container)

			if err := writer.WriteObjectValue("", item); err != nil {
				err = clues.Wrap(err, "writing channel message to serializer").Label(fault.LabelForceNoBackupCreation)
				el.AddRecoverable(ctx, err)

				return
			}

			itemData, err := writer.GetSerializedContent()
			if err != nil {
				err = clues.Wrap(err, "serializing channel message").Label(fault.LabelForceNoBackupCreation)
				el.AddRecoverable(ctx, err)

				return
			}

			info.ParentPath = col.LocationPath().String()

			storeItem, err := data.NewPrefetchedItemWithInfo(
				io.NopCloser(bytes.NewReader(itemData)),
				id,
				details.ItemInfo{Groups: info})
			if err != nil {
				err := clues.StackWC(ctx, err).Label(fault.LabelForceNoBackupCreation)
				el.AddRecoverable(ctx, err)

				return
			}

			col.stream <- storeItem

			atomic.AddInt64(&streamedItems, 1)
			atomic.AddInt64(&totalBytes, info.Size)

			if col.Counter.Inc(count.StreamItemsAdded)%1000 == 0 {
				logger.Ctx(ctx).Infow("item stream progress", "stats", col.Counter.Values())
			}

			col.Counter.Add(count.StreamBytesAdded, info.Size)

			if progressMessage != nil {
				progressMessage <- struct{}{}
			}
		}(id)
	}

	wg.Wait()
}

// -----------------------------------------------------------------------------
// lazyFetchCollection
// -----------------------------------------------------------------------------

type lazyFetchCollection[C graph.GetIDer, I groupsItemer] struct {
	data.BaseCollection
	protectedResource string
	stream            chan data.Item

	contains container[C]

	// added is a list of existing item IDs that were added to a container
	added map[string]time.Time
	// removed is a list of item IDs that were deleted from, or moved out, of a container
	removed map[string]struct{}

	getAndAugment getItemAndAugmentInfoer[C, I]

	statusUpdater support.StatusUpdater
}

func (col *lazyFetchCollection[C, I]) Items(
	ctx context.Context,
	errs *fault.Bus,
) <-chan data.Item {
	go col.streamItems(ctx, errs)
	return col.stream
}

func (col *lazyFetchCollection[C, I]) streamItems(ctx context.Context, errs *fault.Bus) {
	var (
		streamedItems   int64
		wg              sync.WaitGroup
		progressMessage chan<- struct{}
		el              = errs.Local()
	)

	ctx = clues.Add(ctx, "category", col.Category().String())

	defer func() {
		close(col.stream)
		logger.Ctx(ctx).Infow(
			"finished stream backup collection items",
			"stats", col.Counter.Values())

		updateStatus(
			ctx,
			col.statusUpdater,
			len(col.added)+len(col.removed),
			streamedItems,
			0,
			col.FullPath().Folder(false),
			errs.Failure())
	}()

	if len(col.added)+len(col.removed) > 0 {
		progressMessage = observe.CollectionProgress(
			ctx,
			col.Category().HumanString(),
			col.LocationPath().Elements())
		defer close(progressMessage)
	}

	semaphoreCh := make(chan struct{}, col.Opts().Parallelism.ItemFetch)
	defer close(semaphoreCh)

	// delete all removed items
	for id := range col.removed {
		semaphoreCh <- struct{}{}

		wg.Add(1)

		go func(id string) {
			defer wg.Done()
			defer func() { <-semaphoreCh }()

			col.stream <- data.NewDeletedItem(id)

			atomic.AddInt64(&streamedItems, 1)
			col.Counter.Inc(count.StreamItemsRemoved)

			if progressMessage != nil {
				progressMessage <- struct{}{}
			}
		}(id)
	}

	// add any new items
	for id, modTime := range col.added {
		if el.Failure() != nil {
			break
		}

		wg.Add(1)
		semaphoreCh <- struct{}{}

		go func(id string, modTime time.Time) {
			defer wg.Done()
			defer func() { <-semaphoreCh }()

			ictx := clues.Add(
				ctx,
				"item_id", id,
				"parent_path", path.LoggableDir(col.LocationPath().String()))

			col.stream <- data.NewLazyItemWithInfo(
				ictx,
				&lazyItemGetter[C, I]{
					modTime:       modTime,
					getAndAugment: col.getAndAugment,
					resourceID:    col.protectedResource,
					itemID:        id,
					containerIDs:  col.FullPath().Folders(),
					contains:      col.contains,
					parentPath:    col.LocationPath().String(),
				},
				id,
				modTime,
				col.Counter,
				el)

			atomic.AddInt64(&streamedItems, 1)

			if progressMessage != nil {
				progressMessage <- struct{}{}
			}
		}(id, modTime)
	}

	wg.Wait()
}

type lazyItemGetter[C graph.GetIDer, I groupsItemer] struct {
	getAndAugment getItemAndAugmentInfoer[C, I]
	resourceID    string
	itemID        string
	parentPath    string
	containerIDs  path.Elements
	modTime       time.Time
	contains      container[C]
}

func (lig *lazyItemGetter[C, I]) GetData(
	ctx context.Context,
	errs *fault.Bus,
) (io.ReadCloser, *details.ItemInfo, bool, error) {
	writer := kjson.NewJsonSerializationWriter()
	defer writer.Close()

	item, info, err := lig.getAndAugment.getItem(
		ctx,
		lig.resourceID,
		lig.containerIDs,
		lig.itemID)
	if err != nil {
		// For items that were deleted in flight, add the skip label so that
		// they don't lead to recoverable failures during backup.
		if clues.HasLabel(err, graph.LabelStatus(http.StatusNotFound)) || errors.Is(err, core.ErrNotFound) {
			logger.CtxErr(ctx, err).Info("item deleted in flight. skipping")

			// Returning delInFlight as true here for correctness, although the caller is going
			// to ignore it since we are returning an error.
			return nil, nil, true, clues.Wrap(err, "deleted item").Label(graph.LabelsSkippable)
		}

		err = clues.WrapWC(ctx, err, "getting item data").Label(fault.LabelForceNoBackupCreation)
		errs.AddRecoverable(ctx, err)

		return nil, nil, false, err
	}

	lig.getAndAugment.augmentItemInfo(info, lig.contains.container)

	if err := writer.WriteObjectValue("", item); err != nil {
		err = clues.WrapWC(ctx, err, "writing item to serializer").Label(fault.LabelForceNoBackupCreation)
		errs.AddRecoverable(ctx, err)

		return nil, nil, false, err
	}

	itemData, err := writer.GetSerializedContent()
	if err != nil {
		err = clues.WrapWC(ctx, err, "serializing item").Label(fault.LabelForceNoBackupCreation)
		errs.AddRecoverable(ctx, err)

		return nil, nil, false, err
	}

	info.ParentPath = lig.parentPath
	// Update the mod time to what we already told kopia about. This is required
	// for proper details merging.
	info.Modified = lig.modTime

	return io.NopCloser(bytes.NewReader(itemData)),
		&details.ItemInfo{Groups: info},
		false,
		nil
}
