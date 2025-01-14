package m365

import (
	"context"
	"testing"

	"github.com/alcionai/clues"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/microsoftgraph/msgraph-sdk-go/models/odataerrors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/alcionai/corso/src/internal/common/ptr"
	"github.com/alcionai/corso/src/internal/tester"
	"github.com/alcionai/corso/src/internal/tester/tconfig"
	"github.com/alcionai/corso/src/pkg/errs/core"
	"github.com/alcionai/corso/src/pkg/fault"
	"github.com/alcionai/corso/src/pkg/services/m365/api"
	"github.com/alcionai/corso/src/pkg/services/m365/api/graph"
	"github.com/alcionai/corso/src/pkg/services/m365/api/mock"
)

type siteIntegrationSuite struct {
	tester.Suite
	cli client
}

func TestSiteIntegrationSuite(t *testing.T) {
	suite.Run(t, &siteIntegrationSuite{
		Suite: tester.NewIntegrationSuite(
			t,
			[][]string{tconfig.M365AcctCredEnvs}),
	})
}

func (suite *siteIntegrationSuite) SetupSuite() {
	t := suite.T()

	ctx, flush := tester.NewContext(t)
	defer flush()

	acct := tconfig.NewM365Account(t)

	var err error

	// will init the concurrency limiter
	suite.cli, err = NewM365Client(ctx, acct)
	require.NoError(t, err, clues.ToCore(err))
}

func (suite *siteIntegrationSuite) TestSites() {
	t := suite.T()

	ctx, flush := tester.NewContext(t)
	defer flush()

	sites, err := suite.cli.Sites(ctx, fault.New(true))
	assert.NoError(t, err, clues.ToCore(err))
	assert.NotEmpty(t, sites)

	for _, s := range sites {
		suite.Run("site_"+s.ID, func() {
			t := suite.T()
			assert.NotEmpty(t, s.WebURL)
			assert.NotEmpty(t, s.ID)
		})
	}
}

func (suite *siteIntegrationSuite) TestSites_GetByID() {
	t := suite.T()

	ctx, flush := tester.NewContext(t)
	defer flush()

	sites, err := suite.cli.Sites(ctx, fault.New(true))
	assert.NoError(t, err, clues.ToCore(err))
	assert.NotEmpty(t, sites)

	for _, s := range sites {
		suite.Run("site_"+s.ID, func() {
			t := suite.T()
			site, err := suite.cli.SiteByID(ctx, s.ID)
			require.NoError(t, err, clues.ToCore(err))
			assert.NotEmpty(t, site.WebURL)
			assert.NotEmpty(t, site.ID)
			assert.NotEmpty(t, site.OwnerType)
		})
	}
}

// ---------------------------------------------------------------------------
// Unit
// ---------------------------------------------------------------------------

type siteUnitSuite struct {
	tester.Suite
}

func TestSiteUnitSuite(t *testing.T) {
	suite.Run(t, &siteUnitSuite{Suite: tester.NewUnitSuite(t)})
}

type mockGASites struct {
	response []models.Siteable
	err      error
}

func (m mockGASites) GetAll(context.Context, *fault.Bus) ([]models.Siteable, error) {
	return m.response, m.err
}

func (m mockGASites) GetByID(context.Context, string, api.CallConfig) (models.Siteable, error) {
	if len(m.response) == 0 {
		return nil, m.err
	}

	return m.response[0], m.err
}

func (suite *siteUnitSuite) TestGetAllSites() {
	table := []struct {
		name      string
		mock      func(context.Context) getAller[models.Siteable]
		expectErr func(*testing.T, error)
	}{
		{
			name: "ok",
			mock: func(ctx context.Context) getAller[models.Siteable] {
				return mockGASites{[]models.Siteable{}, nil}
			},
			expectErr: func(t *testing.T, err error) {
				assert.NoError(t, err, clues.ToCore(err))
			},
		},
		{
			name: "no sharepoint license",
			mock: func(ctx context.Context) getAller[models.Siteable] {
				odErr := odataerrors.NewODataError()
				merr := odataerrors.NewMainError()
				merr.SetCode(ptr.To("code"))
				merr.SetMessage(ptr.To(string(graph.NoSPLicense)))
				odErr.SetErrorEscaped(merr)

				return mockGASites{nil, graph.Stack(ctx, odErr)}
			},
			expectErr: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, core.ErrServiceNotEnabled, clues.ToCore(err))
			},
		},
		{
			name: "arbitrary error",
			mock: func(ctx context.Context) getAller[models.Siteable] {
				odErr := odataerrors.NewODataError()
				merr := odataerrors.NewMainError()
				merr.SetCode(ptr.To("code"))
				merr.SetMessage(ptr.To("message"))
				odErr.SetErrorEscaped(merr)

				return mockGASites{nil, graph.Stack(ctx, odErr)}
			},
			expectErr: func(t *testing.T, err error) {
				assert.Error(t, err, clues.ToCore(err))
			},
		},
	}
	for _, test := range table {
		suite.Run(test.name, func() {
			t := suite.T()

			ctx, flush := tester.NewContext(t)
			defer flush()

			gas := test.mock(ctx)

			_, err := getAllSites(ctx, gas)
			test.expectErr(t, err)
		})
	}
}

func (suite *siteUnitSuite) TestGetSites() {
	table := []struct {
		name       string
		mock       func(context.Context) api.GetByIDer[models.Siteable]
		expectErr  assert.ErrorAssertionFunc
		expectSite func(*testing.T, *Site)
	}{
		{
			name: "ok - no owner",
			mock: func(ctx context.Context) api.GetByIDer[models.Siteable] {
				return mockGASites{[]models.Siteable{
					mock.DummySite(nil),
				}, nil}
			},
			expectErr: assert.NoError,
			expectSite: func(t *testing.T, site *Site) {
				assert.NotEmpty(t, site.ID)
				assert.NotEmpty(t, site.WebURL)
				assert.Empty(t, site.OwnerID)
			},
		},
		{
			name: "ok - owner user",
			mock: func(ctx context.Context) api.GetByIDer[models.Siteable] {
				return mockGASites{[]models.Siteable{
					mock.DummySite(mock.UserIdentity("userid", "useremail")),
				}, nil}
			},
			expectErr: assert.NoError,
			expectSite: func(t *testing.T, site *Site) {
				assert.NotEmpty(t, site.ID)
				assert.NotEmpty(t, site.WebURL)
				assert.Equal(t, site.OwnerID, "userid")
				assert.Equal(t, site.OwnerEmail, "useremail")
				assert.Equal(t, site.OwnerType, SiteOwnerUser)
			},
		},
		{
			name: "ok - group user with ID and email",
			mock: func(ctx context.Context) api.GetByIDer[models.Siteable] {
				return mockGASites{[]models.Siteable{
					mock.DummySite(mock.GroupIdentitySet("groupid", "groupemail")),
				}, nil}
			},
			expectErr: assert.NoError,
			expectSite: func(t *testing.T, site *Site) {
				assert.NotEmpty(t, site.ID)
				assert.NotEmpty(t, site.WebURL)
				assert.Equal(t, SiteOwnerGroup, site.OwnerType)
				assert.Equal(t, "groupid", site.OwnerID)
				assert.Equal(t, "groupemail", site.OwnerEmail)
			},
		},
		{
			name: "ok - group user with no ID but email",
			mock: func(ctx context.Context) api.GetByIDer[models.Siteable] {
				return mockGASites{[]models.Siteable{
					mock.DummySite(mock.GroupIdentitySet("", "groupemail")),
				}, nil}
			},
			expectErr: assert.NoError,
			expectSite: func(t *testing.T, site *Site) {
				assert.NotEmpty(t, site.ID)
				assert.NotEmpty(t, site.WebURL)
				assert.Equal(t, SiteOwnerGroup, site.OwnerType)
				assert.Equal(t, "", site.OwnerID)
				assert.Equal(t, "groupemail", site.OwnerEmail)
			},
		},
	}
	for _, test := range table {
		suite.Run(test.name, func() {
			t := suite.T()

			ctx, flush := tester.NewContext(t)
			defer flush()

			gas := test.mock(ctx)

			site, err := getSiteByID(ctx, gas, "id", api.CallConfig{})
			test.expectSite(t, site)
			test.expectErr(t, err, clues.ToCore(err))
		})
	}
}
