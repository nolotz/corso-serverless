package exchange

import (
	"context"
	"encoding/json"

	"github.com/alcionai/clues"

	"github.com/alcionai/corso/src/internal/common/idname"
	"github.com/alcionai/corso/src/internal/common/prefixmatcher"
	"github.com/alcionai/corso/src/internal/connector/graph"
	"github.com/alcionai/corso/src/internal/connector/support"
	"github.com/alcionai/corso/src/internal/data"
	"github.com/alcionai/corso/src/internal/observe"
	"github.com/alcionai/corso/src/pkg/control"
	"github.com/alcionai/corso/src/pkg/fault"
	"github.com/alcionai/corso/src/pkg/logger"
	"github.com/alcionai/corso/src/pkg/path"
	"github.com/alcionai/corso/src/pkg/selectors"
	"github.com/alcionai/corso/src/pkg/services/m365/api"
)

// MetadataFileNames produces the category-specific set of filenames used to
// store graph metadata such as delta tokens and folderID->path references.
func MetadataFileNames(cat path.CategoryType) []string {
	switch cat {
	case path.EmailCategory, path.ContactsCategory:
		return []string{graph.DeltaURLsFileName, graph.PreviousPathFileName}
	default:
		return []string{graph.PreviousPathFileName}
	}
}

type CatDeltaPaths map[path.CategoryType]DeltaPaths

type DeltaPaths map[string]DeltaPath

func (dps DeltaPaths) AddDelta(k, d string) {
	dp, ok := dps[k]
	if !ok {
		dp = DeltaPath{}
	}

	dp.Delta = d
	dps[k] = dp
}

func (dps DeltaPaths) AddPath(k, p string) {
	dp, ok := dps[k]
	if !ok {
		dp = DeltaPath{}
	}

	dp.Path = p
	dps[k] = dp
}

type DeltaPath struct {
	Delta string
	Path  string
}

// ParseMetadataCollections produces a map of structs holding delta
// and path lookup maps.
func parseMetadataCollections(
	ctx context.Context,
	colls []data.RestoreCollection,
) (CatDeltaPaths, bool, error) {
	// cdp stores metadata
	cdp := CatDeltaPaths{
		path.ContactsCategory: {},
		path.EmailCategory:    {},
		path.EventsCategory:   {},
	}

	// found tracks the metadata we've loaded, to make sure we don't
	// fetch overlapping copies.
	found := map[path.CategoryType]map[string]struct{}{
		path.ContactsCategory: {},
		path.EmailCategory:    {},
		path.EventsCategory:   {},
	}

	// errors from metadata items should not stop the backup,
	// but it should prevent us from using previous backups
	errs := fault.New(true)

	for _, coll := range colls {
		var (
			breakLoop bool
			items     = coll.Items(ctx, errs)
			category  = coll.FullPath().Category()
		)

		for {
			select {
			case <-ctx.Done():
				return nil, false, clues.Wrap(ctx.Err(), "parsing collection metadata").WithClues(ctx)

			case item, ok := <-items:
				if !ok || errs.Failure() != nil {
					breakLoop = true
					break
				}

				var (
					m    = map[string]string{}
					cdps = cdp[category]
				)

				err := json.NewDecoder(item.ToReader()).Decode(&m)
				if err != nil {
					return nil, false, clues.New("decoding metadata json").WithClues(ctx)
				}

				switch item.UUID() {
				case graph.PreviousPathFileName:
					if _, ok := found[category]["path"]; ok {
						return nil, false, clues.Wrap(clues.New(category.String()), "multiple versions of path metadata").WithClues(ctx)
					}

					for k, p := range m {
						cdps.AddPath(k, p)
					}

					found[category]["path"] = struct{}{}

				case graph.DeltaURLsFileName:
					if _, ok := found[category]["delta"]; ok {
						return nil, false, clues.Wrap(clues.New(category.String()), "multiple versions of delta metadata").WithClues(ctx)
					}

					for k, d := range m {
						cdps.AddDelta(k, d)
					}

					found[category]["delta"] = struct{}{}
				}

				cdp[category] = cdps
			}

			if breakLoop {
				break
			}
		}
	}

	if errs.Failure() != nil {
		logger.CtxErr(ctx, errs.Failure()).Info("reading metadata collection items")

		return CatDeltaPaths{
			path.ContactsCategory: {},
			path.EmailCategory:    {},
			path.EventsCategory:   {},
		}, false, nil
	}

	// Remove any entries that contain a path or a delta, but not both.
	// That metadata is considered incomplete, and needs to incur a
	// complete backup on the next run.
	for _, dps := range cdp {
		for k, dp := range dps {
			if len(dp.Path) == 0 {
				delete(dps, k)
			}
		}
	}

	return cdp, true, nil
}

// DataCollections returns a DataCollection which the caller can
// use to read mailbox data out for the specified user
func DataCollections(
	ctx context.Context,
	ac api.Client,
	selector selectors.Selector,
	tenantID string,
	user idname.Provider,
	metadata []data.RestoreCollection,
	su support.StatusUpdater,
	ctrlOpts control.Options,
	errs *fault.Bus,
) ([]data.BackupCollection, *prefixmatcher.StringSetMatcher, bool, error) {
	eb, err := selector.ToExchangeBackup()
	if err != nil {
		return nil, nil, false, clues.Wrap(err, "exchange dataCollection selector").WithClues(ctx)
	}

	var (
		collections = []data.BackupCollection{}
		el          = errs.Local()
		categories  = map[path.CategoryType]struct{}{}
		handlers    = BackupHandlers(ac)
	)

	// Turn on concurrency limiter middleware for exchange backups
	// unless explicitly disabled through DisableConcurrencyLimiterFN cli flag
	if !ctrlOpts.ToggleFeatures.DisableConcurrencyLimiter {
		graph.InitializeConcurrencyLimiter(ctrlOpts.Parallelism.ItemFetch)
	}

	cdps, canUsePreviousBackup, err := parseMetadataCollections(ctx, metadata)
	if err != nil {
		return nil, nil, false, err
	}

	for _, scope := range eb.Scopes() {
		if el.Failure() != nil {
			break
		}

		dcs, err := createCollections(
			ctx,
			handlers,
			tenantID,
			user,
			scope,
			cdps[scope.Category().PathType()],
			ctrlOpts,
			su,
			errs)
		if err != nil {
			el.AddRecoverable(err)
			continue
		}

		categories[scope.Category().PathType()] = struct{}{}

		collections = append(collections, dcs...)
	}

	if len(collections) > 0 {
		baseCols, err := graph.BaseCollections(
			ctx,
			collections,
			tenantID,
			user.ID(),
			path.ExchangeService,
			categories,
			su,
			errs)
		if err != nil {
			return nil, nil, false, err
		}

		collections = append(collections, baseCols...)
	}

	return collections, nil, canUsePreviousBackup, el.Failure()
}

// createCollections - utility function that retrieves M365
// IDs through Microsoft Graph API. The selectors.ExchangeScope
// determines the type of collections that are retrieved.
func createCollections(
	ctx context.Context,
	handlers map[path.CategoryType]backupHandler,
	tenantID string,
	user idname.Provider,
	scope selectors.ExchangeScope,
	dps DeltaPaths,
	ctrlOpts control.Options,
	su support.StatusUpdater,
	errs *fault.Bus,
) ([]data.BackupCollection, error) {
	ctx = clues.Add(ctx, "category", scope.Category().PathType())

	var (
		allCollections = make([]data.BackupCollection, 0)
		category       = scope.Category().PathType()
		qp             = graph.QueryParams{
			Category:      category,
			ResourceOwner: user,
			TenantID:      tenantID,
		}
	)

	handler, ok := handlers[category]
	if !ok {
		return nil, clues.New("unsupported backup category type").WithClues(ctx)
	}

	foldersComplete := observe.MessageWithCompletion(
		ctx,
		observe.Bulletf("%s", qp.Category))
	defer close(foldersComplete)

	rootFolder, cc := handler.NewContainerCache(user.ID())

	if err := cc.Populate(ctx, errs, rootFolder); err != nil {
		return nil, clues.Wrap(err, "populating container cache")
	}

	collections, err := filterContainersAndFillCollections(
		ctx,
		qp,
		handler,
		su,
		cc,
		scope,
		dps,
		ctrlOpts,
		errs)
	if err != nil {
		return nil, clues.Wrap(err, "filling collections")
	}

	foldersComplete <- struct{}{}

	for _, coll := range collections {
		allCollections = append(allCollections, coll)
	}

	return allCollections, nil
}
