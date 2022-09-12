package path_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/alcionai/corso/src/internal/path"
)

const (
	testTenant = "aTenant"
	testUser   = "aUser"
)

var (
	// Purposely doesn't have characters that need escaping so it can be easily
	// computed using strings.Join().
	rest = []string{"some", "folder", "path", "with", "possible", "item"}

	missingInfo = []struct {
		name   string
		tenant string
		user   string
		rest   []string
	}{
		{
			name:   "NoTenant",
			tenant: "",
			user:   testUser,
			rest:   rest,
		},
		{
			name:   "NoResourceOwner",
			tenant: testTenant,
			user:   "",
			rest:   rest,
		},
		{
			name:   "NoFolderOrItem",
			tenant: testTenant,
			user:   testUser,
			rest:   nil,
		},
	}

	modes = []struct {
		name           string
		isItem         bool
		expectedFolder string
		expectedItem   string
	}{
		{
			name:           "Folder",
			isItem:         false,
			expectedFolder: strings.Join(rest, "/"),
			expectedItem:   "",
		},
		{
			name:           "Item",
			isItem:         true,
			expectedFolder: strings.Join(rest[0:len(rest)-1], "/"),
			expectedItem:   rest[len(rest)-1],
		},
	}

	// Set of acceptable service/category mixtures for exchange.
	exchangeServiceCategories = []struct {
		service  path.ServiceType
		category path.CategoryType
	}{
		{
			service:  path.ExchangeService,
			category: path.EmailCategory,
		},
		{
			service:  path.ExchangeService,
			category: path.ContactsCategory,
		},
		{
			service:  path.ExchangeService,
			category: path.EventsCategory,
		},
	}
)

type DataLayerResourcePath struct {
	suite.Suite
}

func TestDataLayerResourcePath(t *testing.T) {
	suite.Run(t, new(DataLayerResourcePath))
}

func (suite *DataLayerResourcePath) TestMissingInfoErrors() {
	for _, types := range exchangeServiceCategories {
		suite.T().Run(types.service.String()+types.category.String(), func(t1 *testing.T) {
			for _, m := range modes {
				t1.Run(m.name, func(t2 *testing.T) {
					for _, test := range missingInfo {
						t2.Run(test.name, func(t *testing.T) {
							b := path.Builder{}.Append(test.rest...)

							_, err := b.ToDataLayerExchangePathForCategory(
								test.tenant,
								test.user,
								types.category,
								m.isItem,
							)
							assert.Error(t, err)
						})
					}
				})
			}
		})
	}
}

func (suite *DataLayerResourcePath) TestMailItemNoFolder() {
	item := "item"
	b := path.Builder{}.Append(item)

	for _, types := range exchangeServiceCategories {
		suite.T().Run(types.service.String()+types.category.String(), func(t *testing.T) {
			p, err := b.ToDataLayerExchangePathForCategory(
				testTenant,
				testUser,
				types.category,
				true,
			)
			require.NoError(t, err)

			assert.Empty(t, p.Folder())
			assert.Equal(t, item, p.Item())
		})
	}
}

func (suite *DataLayerResourcePath) TestToExchangePathForCategory() {
	b := path.Builder{}.Append(rest...)
	table := []struct {
		category path.CategoryType
		check    assert.ErrorAssertionFunc
	}{
		{
			category: path.UnknownCategory,
			check:    assert.Error,
		},
		{
			category: path.CategoryType(-1),
			check:    assert.Error,
		},
		{
			category: path.EmailCategory,
			check:    assert.NoError,
		},
		{
			category: path.ContactsCategory,
			check:    assert.NoError,
		},
		{
			category: path.EventsCategory,
			check:    assert.NoError,
		},
	}

	for _, m := range modes {
		suite.T().Run(m.name, func(t1 *testing.T) {
			for _, test := range table {
				t1.Run(test.category.String(), func(t *testing.T) {
					p, err := b.ToDataLayerExchangePathForCategory(
						testTenant,
						testUser,
						test.category,
						m.isItem,
					)

					test.check(t, err)

					if err != nil {
						return
					}

					assert.Equal(t, testTenant, p.Tenant())
					assert.Equal(t, path.ExchangeService, p.Service())
					assert.Equal(t, test.category, p.Category())
					assert.Equal(t, testUser, p.ResourceOwner())
					assert.Equal(t, m.expectedFolder, p.Folder())
					assert.Equal(t, m.expectedItem, p.Item())
				})
			}
		})
	}
}

type PopulatedDataLayerResourcePath struct {
	suite.Suite
	// Bool value is whether the path is an item path or a folder path.
	paths map[bool]path.Path
}

func TestPopulatedDataLayerResourcePath(t *testing.T) {
	suite.Run(t, new(PopulatedDataLayerResourcePath))
}

func (suite *PopulatedDataLayerResourcePath) SetupSuite() {
	suite.paths = make(map[bool]path.Path, 2)
	base := path.Builder{}.Append(rest...)

	for _, t := range []bool{true, false} {
		p, err := base.ToDataLayerExchangePathForCategory(
			testTenant,
			testUser,
			path.EmailCategory,
			t,
		)
		require.NoError(suite.T(), err)

		suite.paths[t] = p
	}
}

func (suite *PopulatedDataLayerResourcePath) TestTenant() {
	for _, m := range modes {
		suite.T().Run(m.name, func(t *testing.T) {
			assert.Equal(t, testTenant, suite.paths[m.isItem].Tenant())
		})
	}
}

func (suite *PopulatedDataLayerResourcePath) TestService() {
	for _, m := range modes {
		suite.T().Run(m.name, func(t *testing.T) {
			assert.Equal(t, path.ExchangeService, suite.paths[m.isItem].Service())
		})
	}
}

func (suite *PopulatedDataLayerResourcePath) TestCategory() {
	for _, m := range modes {
		suite.T().Run(m.name, func(t *testing.T) {
			assert.Equal(t, path.EmailCategory, suite.paths[m.isItem].Category())
		})
	}
}

func (suite *PopulatedDataLayerResourcePath) TestResourceOwner() {
	for _, m := range modes {
		suite.T().Run(m.name, func(t *testing.T) {
			assert.Equal(t, testUser, suite.paths[m.isItem].ResourceOwner())
		})
	}
}

func (suite *PopulatedDataLayerResourcePath) TestFolder() {
	for _, m := range modes {
		suite.T().Run(m.name, func(t *testing.T) {
			assert.Equal(t, m.expectedFolder, suite.paths[m.isItem].Folder())
		})
	}
}

func (suite *PopulatedDataLayerResourcePath) TestItem() {
	for _, m := range modes {
		suite.T().Run(m.name, func(t *testing.T) {
			assert.Equal(t, m.expectedItem, suite.paths[m.isItem].Item())
		})
	}
}
