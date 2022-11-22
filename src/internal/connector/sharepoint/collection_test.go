package sharepoint

import (
	"bytes"
	"io"
	"testing"

	kw "github.com/microsoft/kiota-serialization-json-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/alcionai/corso/src/internal/connector/mockconnector"
	"github.com/alcionai/corso/src/internal/data"
	"github.com/alcionai/corso/src/internal/tester"
	"github.com/alcionai/corso/src/pkg/path"
)

type SharePointCollectionSuite struct {
	suite.Suite
}

func TestSharePointCollectionSuite(t *testing.T) {
	suite.Run(t, new(SharePointCollectionSuite))
}

func (suite *SharePointCollectionSuite) TestSharePointDataReader_Valid() {
	t := suite.T()
	m := []byte("test message")
	name := "aFile"
	sc := &Item{
		id:   name,
		data: io.NopCloser(bytes.NewReader(m)),
	}
	readData, err := io.ReadAll(sc.ToReader())
	require.NoError(t, err)

	assert.Equal(t, name, sc.id)
	assert.Equal(t, readData, m)
}

// TestSharePointListCollection tests basic functionality to create
// SharePoint collection and to use the data stream channel.
func (suite *SharePointCollectionSuite) TestSharePointListCollection() {
	t := suite.T()
	ctx, flush := tester.NewContext()

	defer flush()

	ow := kw.NewJsonSerializationWriter()
	listing := mockconnector.GetMockList("Mock List")
	testName := "MockListing"
	listing.SetDisplayName(&testName)

	err := ow.WriteObjectValue("", listing)
	require.NoError(t, err)

	byteArray, err := ow.GetSerializedContent()
	require.NoError(t, err)
	// TODO: Replace with Sharepoint--> ToDataLayerSharePoint
	// https://github.com/alcionai/corso/issues/1401
	dir, err := path.Builder{}.Append("directory").
		ToDataLayerExchangePathForCategory(
			"some",
			"user",
			path.EmailCategory,
			false)
	require.NoError(t, err)

	col := NewCollection(dir, nil, nil)
	col.data <- &Item{
		id:   testName,
		data: io.NopCloser(bytes.NewReader(byteArray)),
		info: sharePointListInfo(listing, int64(len(byteArray))),
	}
	col.finishPopulation(ctx, 0, 0, nil)

	readItems := []data.Stream{}
	for item := range col.Items() {
		readItems = append(readItems, item)
	}

	require.Equal(t, len(readItems), 1)
	item := readItems[0]
	shareInfo, ok := item.(data.StreamInfo)
	require.True(t, ok)
	require.NotNil(t, shareInfo.Info())
	require.NotNil(t, shareInfo.Info().SharePoint)
	assert.Equal(t, testName, shareInfo.Info().SharePoint.ItemName)
}