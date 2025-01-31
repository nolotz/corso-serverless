package api

import (
	"fmt"
	"testing"
	"time"

	"github.com/alcionai/clues"
	"github.com/h2non/gock"
	kjson "github.com/microsoft/kiota-serialization-json-go"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/alcionai/corso/src/internal/common/ptr"
	spMock "github.com/alcionai/corso/src/internal/m365/service/sharepoint/mock"
	"github.com/alcionai/corso/src/internal/tester"
	"github.com/alcionai/corso/src/internal/tester/tconfig"
	"github.com/alcionai/corso/src/pkg/backup/details"
	"github.com/alcionai/corso/src/pkg/control/testdata"
	"github.com/alcionai/corso/src/pkg/errs/core"
	"github.com/alcionai/corso/src/pkg/fault"
	graphTD "github.com/alcionai/corso/src/pkg/services/m365/api/graph/testdata"
)

type ListsUnitSuite struct {
	tester.Suite
}

func TestListsUnitSuite(t *testing.T) {
	suite.Run(t, &ListsUnitSuite{Suite: tester.NewUnitSuite(t)})
}

func (suite *ListsUnitSuite) TestSharePointInfo() {
	tests := []struct {
		name         string
		listAndDeets func() (models.Listable, *details.SharePointInfo)
	}{
		{
			name: "Empty List",
			listAndDeets: func() (models.Listable, *details.SharePointInfo) {
				i := &details.SharePointInfo{ItemType: details.SharePointList}
				return models.NewList(), i
			},
		}, {
			name: "Only Name",
			listAndDeets: func() (models.Listable, *details.SharePointInfo) {
				aTitle := "Whole List"
				listing := models.NewList()
				listing.SetDisplayName(&aTitle)

				li := models.NewListItem()
				li.SetId(ptr.To("listItem1"))

				listing.SetItems([]models.ListItemable{li})
				i := &details.SharePointInfo{
					ItemType: details.SharePointList,
					List: &details.ListInfo{
						Name:      aTitle,
						ItemCount: 1,
					},
				}

				return listing, i
			},
		},
	}

	for _, test := range tests {
		suite.Run(test.name, func() {
			t := suite.T()

			list, expected := test.listAndDeets()
			info := ListToSPInfo(list)
			assert.Equal(t, expected.ItemType, info.ItemType)
			assert.Equal(t, expected.ItemName, info.ItemName)
			assert.Equal(t, expected.WebURL, info.WebURL)
			if expected.List != nil {
				assert.Equal(t, expected.List.ItemCount, info.List.ItemCount)
			}
		})
	}
}

func (suite *ListsUnitSuite) TestBytesToListable() {
	listBytes, err := spMock.ListBytes("DataSupportSuite")
	require.NoError(suite.T(), err)

	tests := []struct {
		name       string
		byteArray  []byte
		checkError assert.ErrorAssertionFunc
		isNil      assert.ValueAssertionFunc
	}{
		{
			name:       "empty bytes",
			byteArray:  make([]byte, 0),
			checkError: assert.Error,
			isNil:      assert.Nil,
		},
		{
			name:       "invalid bytes",
			byteArray:  []byte("Invalid byte stream \"subject:\" Not going to work"),
			checkError: assert.Error,
			isNil:      assert.Nil,
		},
		{
			name:       "Valid List",
			byteArray:  listBytes,
			checkError: assert.NoError,
			isNil:      assert.NotNil,
		},
	}

	for _, test := range tests {
		suite.Run(test.name, func() {
			t := suite.T()

			result, err := BytesToListable(test.byteArray)
			test.checkError(t, err, clues.ToCore(err))
			test.isNil(t, result)
		})
	}
}

func (suite *ListsUnitSuite) TestColumnDefinitionable_GetValidation() {
	tests := []struct {
		name    string
		getOrig func() models.ColumnDefinitionable
		expect  assert.ValueAssertionFunc
	}{
		{
			name: "column validation not set",
			getOrig: func() models.ColumnDefinitionable {
				textColumn := models.NewTextColumn()

				cd := models.NewColumnDefinition()
				cd.SetText(textColumn)

				return cd
			},
			expect: assert.Nil,
		},
		{
			name: "column validation set",
			getOrig: func() models.ColumnDefinitionable {
				textColumn := models.NewTextColumn()

				colValidation := models.NewColumnValidation()

				cd := models.NewColumnDefinition()
				cd.SetText(textColumn)
				cd.SetValidation(colValidation)

				return cd
			},
			expect: assert.NotNil,
		},
	}

	for _, test := range tests {
		suite.Run(test.name, func() {
			t := suite.T()

			orig := test.getOrig()
			newCd := cloneColumnDefinitionable(orig)

			require.NotEmpty(t, newCd)

			test.expect(t, newCd.GetValidation())
		})
	}
}

func (suite *ListsUnitSuite) TestColumnDefinitionable_GetDefaultValue() {
	tests := []struct {
		name    string
		getOrig func() models.ColumnDefinitionable
		expect  func(t *testing.T, cd models.ColumnDefinitionable)
	}{
		{
			name: "column default value not set",
			getOrig: func() models.ColumnDefinitionable {
				textColumn := models.NewTextColumn()

				cd := models.NewColumnDefinition()
				cd.SetText(textColumn)

				return cd
			},
			expect: func(t *testing.T, cd models.ColumnDefinitionable) {
				assert.Nil(t, cd.GetDefaultValue())
			},
		},
		{
			name: "column default value set",
			getOrig: func() models.ColumnDefinitionable {
				defaultVal := "some-val"

				textColumn := models.NewTextColumn()

				colDefaultVal := models.NewDefaultColumnValue()
				colDefaultVal.SetValue(ptr.To(defaultVal))

				cd := models.NewColumnDefinition()
				cd.SetText(textColumn)
				cd.SetDefaultValue(colDefaultVal)

				return cd
			},
			expect: func(t *testing.T, cd models.ColumnDefinitionable) {
				assert.NotNil(t, cd.GetDefaultValue())
				assert.Equal(t, "some-val", ptr.Val(cd.GetDefaultValue().GetValue()))
			},
		},
	}

	for _, test := range tests {
		suite.Run(test.name, func() {
			t := suite.T()

			orig := test.getOrig()
			newCd := cloneColumnDefinitionable(orig)

			require.NotEmpty(t, newCd)
			test.expect(t, newCd)
		})
	}
}

func (suite *ListsUnitSuite) TestColumnDefinitionable_ColumnType() {
	tests := []struct {
		name    string
		getOrig func() models.ColumnDefinitionable
		checkFn func(models.ColumnDefinitionable) bool
	}{
		{
			name: "column type should be number",
			getOrig: func() models.ColumnDefinitionable {
				numColumn := models.NewNumberColumn()

				cd := models.NewColumnDefinition()
				cd.SetNumber(numColumn)

				return cd
			},
			checkFn: func(cd models.ColumnDefinitionable) bool {
				return cd.GetNumber() != nil
			},
		},
		{
			name: "column type should be person or group",
			getOrig: func() models.ColumnDefinitionable {
				pgColumn := models.NewPersonOrGroupColumn()

				cd := models.NewColumnDefinition()
				cd.SetPersonOrGroup(pgColumn)

				return cd
			},
			checkFn: func(cd models.ColumnDefinitionable) bool {
				return cd.GetPersonOrGroup() != nil
			},
		},
		{
			name: "column type should default to text",
			getOrig: func() models.ColumnDefinitionable {
				return models.NewColumnDefinition()
			},
			checkFn: func(cd models.ColumnDefinitionable) bool {
				return cd.GetText() != nil
			},
		},
	}

	for _, test := range tests {
		suite.Run(test.name, func() {
			t := suite.T()

			orig := test.getOrig()
			newCd := cloneColumnDefinitionable(orig)

			require.NotEmpty(t, newCd)
			assert.True(t, test.checkFn(newCd))
		})
	}
}

func (suite *ListsUnitSuite) TestColumnDefinitionable_LegacyColumns() {
	listName := "test-list"
	textColumnName := "ItemName"
	textColumnDisplayName := "Item Name"
	titleColumnName := "Title"
	titleColumnDisplayName := "Title"
	readOnlyColumnName := "TestColumn"
	readOnlyColumnDisplayName := "Test Column"

	contentTypeCd := models.NewColumnDefinition()
	contentTypeCd.SetName(ptr.To(ContentTypeColumnName))
	contentTypeCd.SetDisplayName(ptr.To(ContentTypeColumnDisplayName))

	attachmentCd := models.NewColumnDefinition()
	attachmentCd.SetName(ptr.To(AttachmentsColumnName))
	attachmentCd.SetDisplayName(ptr.To(AttachmentsColumnName))

	editCd := models.NewColumnDefinition()
	editCd.SetName(ptr.To(EditColumnName))
	editCd.SetDisplayName(ptr.To(EditColumnName))

	textCol := models.NewTextColumn()
	titleCol := models.NewTextColumn()
	roCol := models.NewTextColumn()

	textCd := models.NewColumnDefinition()
	textCd.SetName(ptr.To(textColumnName))
	textCd.SetDisplayName(ptr.To(textColumnDisplayName))
	textCd.SetText(textCol)

	titleCd := models.NewColumnDefinition()
	titleCd.SetName(ptr.To(titleColumnName))
	titleCd.SetDisplayName(ptr.To(titleColumnDisplayName))
	titleCd.SetText(titleCol)

	roCd := models.NewColumnDefinition()
	roCd.SetName(ptr.To(readOnlyColumnName))
	roCd.SetDisplayName(ptr.To(readOnlyColumnDisplayName))
	roCd.SetText(roCol)
	roCd.SetReadOnly(ptr.To(true))

	tests := []struct {
		name             string
		getList          func() *models.List
		length           int
		expectedColNames map[string]any
	}{
		{
			name: "all legacy columns",
			getList: func() *models.List {
				lst := models.NewList()
				lst.SetColumns([]models.ColumnDefinitionable{
					contentTypeCd,
					attachmentCd,
					editCd,
				})
				return lst
			},
			length:           0,
			expectedColNames: map[string]any{TitleColumnName: nil},
		},
		{
			name: "title and legacy columns",
			getList: func() *models.List {
				lst := models.NewList()
				lst.SetColumns([]models.ColumnDefinitionable{
					contentTypeCd,
					attachmentCd,
					editCd,
					titleCd,
				})
				return lst
			},
			length:           0,
			expectedColNames: map[string]any{TitleColumnName: nil},
		},
		{
			name: "readonly and legacy columns",
			getList: func() *models.List {
				lst := models.NewList()
				lst.SetColumns([]models.ColumnDefinitionable{
					contentTypeCd,
					attachmentCd,
					editCd,
					roCd,
				})
				return lst
			},
			length:           0,
			expectedColNames: map[string]any{TitleColumnName: nil},
		},
		{
			name: "legacy and a text column",
			getList: func() *models.List {
				lst := models.NewList()
				lst.SetColumns([]models.ColumnDefinitionable{
					contentTypeCd,
					attachmentCd,
					editCd,
					textCd,
				})
				return lst
			},
			length: 1,
			expectedColNames: map[string]any{
				TitleColumnName: nil,
				textColumnName:  nil,
			},
		},
	}

	for _, test := range tests {
		suite.Run(test.name, func() {
			t := suite.T()

			clonedList, colNames := ToListable(test.getList(), listName)
			require.NotEmpty(t, clonedList)
			assert.Equal(t, test.expectedColNames, colNames)

			cols := clonedList.GetColumns()
			assert.Len(t, cols, test.length)
		})
	}
}

func (suite *ListsUnitSuite) TestFieldValueSetable() {
	t := suite.T()

	additionalData := map[string]any{
		DescoratorFieldNamePrefix + "odata.etag":            "14fe12b2-e180-49f7-8fc3-5936f3dcf5d2,1",
		ReadOnlyOrHiddenFieldNamePrefix + "UIVersionString": "1.0",
		AuthorLookupIDColumnName:                            "6",
		EditorLookupIDColumnName:                            "6",
		AppAuthorLookupIDColumnName:                         "6",
		"Item" + ChildCountFieldNamePart:                    "0",
		"Folder" + ChildCountFieldNamePart:                  "0",
		ModifiedColumnName:                                  "2023-12-13T15:47:51Z",
		CreatedColumnName:                                   "2023-12-13T15:47:51Z",
		EditColumnName:                                      "",
		LinkTitleFieldNamePart + "NoMenu":                   "Person1",
	}

	origFs := models.NewFieldValueSet()
	origFs.SetAdditionalData(additionalData)

	colNames := map[string]any{}

	fs := retrieveFieldData(origFs, colNames)
	fsAdditionalData := fs.GetAdditionalData()
	assert.Empty(t, fsAdditionalData)

	additionalData["itemName"] = "item-1"
	origFs = models.NewFieldValueSet()
	origFs.SetAdditionalData(additionalData)

	colNames["itemName"] = struct{}{}

	fs = retrieveFieldData(origFs, colNames)
	fsAdditionalData = fs.GetAdditionalData()
	assert.NotEmpty(t, fsAdditionalData)

	val, ok := fsAdditionalData["itemName"]
	assert.True(t, ok)
	assert.Equal(t, "item-1", val)
}

func (suite *ListsUnitSuite) TestFieldValueSetable_Location() {
	t := suite.T()

	displayName := "B123 Unit 1852 Prime Residences Tagaytay"
	street := "Prime Residences CityLand 1852"
	state := "Calabarzon"
	postal := "4120"
	country := "Philippines"
	city := "Tagaytay"
	lat := 14.1153
	lon := 120.962

	additionalData := map[string]any{
		"MyAddress": map[string]any{
			AddressKey: map[string]any{
				CityKey:       ptr.To(city),
				CountryKey:    ptr.To(country),
				PostalCodeKey: ptr.To(postal),
				StateKey:      ptr.To(state),
				StreetKey:     ptr.To(street),
			},
			CoordinatesKey: map[string]any{
				LatitudeKey:  ptr.To(lat),
				LongitudeKey: ptr.To(lon),
			},
			DisplayNameKey: ptr.To(displayName),
			LocationURIKey: ptr.To("https://www.bingapis.com/api/v6/localbusinesses/YN8144x496766267081923032"),
			UniqueIDKey:    ptr.To("https://www.bingapis.com/api/v6/localbusinesses/YN8144x496766267081923032"),
		},
		CountryOrRegionFN: ptr.To(country),
		StateFN:           ptr.To(state),
		CityFN:            ptr.To(city),
		PostalCodeFN:      ptr.To(postal),
		StreetFN:          ptr.To(street),
		GeoLocFN: map[string]any{
			"latitude":  ptr.To(lat),
			"longitude": ptr.To(lon),
		},
		DispNameFN: ptr.To(displayName),
	}

	expectedData := map[string]any{
		"MyAddress": fmt.Sprintf("%s,%s,%s,%s,%s,%s,%v,%v",
			displayName,
			street,
			city,
			state,
			country,
			postal,
			lat,
			lon),
	}

	origFs := models.NewFieldValueSet()
	origFs.SetAdditionalData(additionalData)

	colNames := map[string]any{
		"MyAddress": nil,
	}

	fs := retrieveFieldData(origFs, colNames)
	fsAdditionalData := fs.GetAdditionalData()
	assert.Equal(t, expectedData, fsAdditionalData)
}

func (suite *ListsUnitSuite) TestConcatenateAddressFields() {
	t := suite.T()

	tests := []struct {
		name           string
		addressFields  map[string]any
		expectedResult string
	}{
		{
			name: "Valid Address",
			addressFields: map[string]any{
				DisplayNameKey: ptr.To("John Doe"),
				AddressKey: map[string]any{
					StreetKey:     ptr.To("123 Main St"),
					CityKey:       ptr.To("Cityville"),
					StateKey:      ptr.To("State"),
					CountryKey:    ptr.To("Country"),
					PostalCodeKey: ptr.To("12345"),
				},
				CoordinatesKey: map[string]any{
					LatitudeKey:  ptr.To(40.7128),
					LongitudeKey: ptr.To(-74.0060),
				},
			},
			expectedResult: "John Doe,123 Main St,Cityville,State,Country,12345,40.7128,-74.006",
		},
		{
			name: "Empty Address Fields",
			addressFields: map[string]any{
				DisplayNameKey: ptr.To("John Doe"),
			},
			expectedResult: "John Doe",
		},
		{
			name:           "Empty Input",
			addressFields:  map[string]any{},
			expectedResult: "",
		},
	}

	for _, test := range tests {
		suite.Run(test.name, func() {
			result := concatenateAddressFields(test.addressFields)
			assert.Equal(t, test.expectedResult, result, "address should match")
		})
	}
}

func (suite *ListsUnitSuite) TestHasAddressFields() {
	t := suite.T()

	tests := []struct {
		name           string
		additionalData map[string]any
		expectedFields map[string]any
		expectedName   string
		expectedFound  bool
	}{
		{
			name: "Address Fields Found",
			additionalData: map[string]any{
				"person1": map[string]any{
					AddressKey: map[string]any{
						StreetKey:     "123 Main St",
						CityKey:       "Cityville",
						StateKey:      "State",
						CountryKey:    "Country",
						PostalCodeKey: "12345",
					},
					CoordinatesKey: map[string]any{
						LatitudeKey:  "40.7128",
						LongitudeKey: "-74.0060",
					},
					DisplayNameKey: "John Doe",
					LocationURIKey: "some loc",
					UniqueIDKey:    "some id",
				},
			},
			expectedFields: map[string]any{
				AddressKey: map[string]any{
					StreetKey:     "123 Main St",
					CityKey:       "Cityville",
					StateKey:      "State",
					CountryKey:    "Country",
					PostalCodeKey: "12345",
				},
				CoordinatesKey: map[string]any{
					LatitudeKey:  "40.7128",
					LongitudeKey: "-74.0060",
				},
				DisplayNameKey: "John Doe",
				LocationURIKey: "some loc",
				UniqueIDKey:    "some id",
			},
			expectedName:  "person1",
			expectedFound: true,
		},
		{
			name: "No Address Fields",
			additionalData: map[string]any{
				"person1": map[string]any{
					"name": "John Doe",
					"age":  30,
				},
				"person2": map[string]any{
					"name": "Jane Doe",
					"age":  25,
				},
			},
			expectedFields: nil,
			expectedName:   "",
			expectedFound:  false,
		},
		{
			name:           "Empty Input",
			additionalData: map[string]any{},
			expectedFields: nil,
			expectedName:   "",
			expectedFound:  false,
		},
	}

	for _, test := range tests {
		suite.Run(test.name, func() {
			fields, fieldName, found := hasAddressFields(test.additionalData)
			require.Equal(t, test.expectedFound, found, "address fields identification should match")
			assert.Equal(t, test.expectedName, fieldName, "address field name should match")
			assert.Equal(t, test.expectedFields, fields, "address fields should match")
		})
	}
}

func (suite *ListsUnitSuite) TestConcatenateHyperlinkFields() {
	t := suite.T()

	tests := []struct {
		name            string
		hyperlinkFields map[string]any
		expectedResult  string
	}{
		{
			name: "Valid Hyperlink",
			hyperlinkFields: map[string]any{
				HyperlinkURLKey:         ptr.To("https://www.example.com"),
				HyperlinkDescriptionKey: ptr.To("Example Website"),
			},
			expectedResult: "https://www.example.com,Example Website",
		},
		{
			name: "Empty Hyperlink Fields",
			hyperlinkFields: map[string]any{
				HyperlinkURLKey:         nil,
				HyperlinkDescriptionKey: nil,
			},
			expectedResult: "",
		},
		{
			name: "Missing Description",
			hyperlinkFields: map[string]any{
				HyperlinkURLKey: ptr.To("https://www.example.com"),
			},
			expectedResult: "https://www.example.com",
		},
		{
			name: "Missing URL",
			hyperlinkFields: map[string]any{
				HyperlinkDescriptionKey: ptr.To("Example Website"),
			},
			expectedResult: "Example Website",
		},
		{
			name:            "Empty Input",
			hyperlinkFields: map[string]any{},
			expectedResult:  "",
		},
	}

	for _, test := range tests {
		suite.Run(test.name, func() {
			result := concatenateHyperLinkFields(test.hyperlinkFields)
			assert.Equal(t, test.expectedResult, result)
		})
	}
}

type ListsAPIIntgSuite struct {
	tester.Suite
	its intgTesterSetup
}

func (suite *ListsAPIIntgSuite) SetupSuite() {
	suite.its = newIntegrationTesterSetup(suite.T())
}

func TestListsAPIIntgSuite(t *testing.T) {
	suite.Run(t, &ListsAPIIntgSuite{
		Suite: tester.NewIntegrationSuite(
			t,
			[][]string{tconfig.M365AcctCredEnvs}),
	})
}

func (suite *ListsAPIIntgSuite) TestLists_PostDrive() {
	t := suite.T()

	ctx, flush := tester.NewContext(t)
	defer flush()

	var (
		acl       = suite.its.ac.Lists()
		driveName = testdata.DefaultRestoreConfig("list_api_post_drive").Location
		siteID    = suite.its.site.id
	)

	// first post, should have no errors
	list, err := acl.PostDrive(ctx, siteID, driveName)
	require.NoError(t, err, clues.ToCore(err))
	// the site name cannot be set when posting, only its DisplayName.
	// so we double check here that we're still getting the name we expect.
	assert.Equal(t, driveName, ptr.Val(list.GetName()))

	// second post, same name, should error on name conflict]
	_, err = acl.PostDrive(ctx, siteID, driveName)
	require.ErrorIs(t, err, core.ErrAlreadyExists, clues.ToCore(err))
}

func (suite *ListsAPIIntgSuite) TestLists_GetListByID() {
	var (
		listID            = "fake-list-id"
		listName          = "fake-list-name"
		listTemplate      = "genericList"
		siteID            = suite.its.site.id
		textColumnDefID   = "fake-text-column-id"
		textColumnDefName = "itemName"
		numColumnDefID    = "fake-num-column-id"
		numColumnDefName  = "itemSize"
		colLinkID         = "fake-collink-id"
		cTypeID           = "fake-ctype-id"
		listItemID        = "fake-list-item-id"
	)

	tests := []struct {
		name   string
		setupf func()
		expect assert.ErrorAssertionFunc
	}{
		{
			name: "",
			setupf: func() {
				listInfo := models.NewListInfo()
				listInfo.SetTemplate(ptr.To(listTemplate))

				list := models.NewList()
				list.SetId(ptr.To(listID))
				list.SetDisplayName(ptr.To(listName))
				list.SetList(listInfo)
				list.SetCreatedDateTime(ptr.To(time.Now()))
				list.SetLastModifiedDateTime(ptr.To(time.Now()))

				txtColumnDef := models.NewColumnDefinition()
				txtColumnDef.SetId(&textColumnDefID)
				txtColumnDef.SetName(&textColumnDefName)
				textColumn := models.NewTextColumn()
				txtColumnDef.SetText(textColumn)
				columnDefCol := models.NewColumnDefinitionCollectionResponse()
				columnDefCol.SetValue([]models.ColumnDefinitionable{txtColumnDef})

				numColumnDef := models.NewColumnDefinition()
				numColumnDef.SetId(&numColumnDefID)
				numColumnDef.SetName(&numColumnDefName)
				numColumn := models.NewNumberColumn()
				numColumnDef.SetNumber(numColumn)
				columnDefCol2 := models.NewColumnDefinitionCollectionResponse()
				columnDefCol2.SetValue([]models.ColumnDefinitionable{numColumnDef})

				colLink := models.NewColumnLink()
				colLink.SetId(&colLinkID)
				colLinkCol := models.NewColumnLinkCollectionResponse()
				colLinkCol.SetValue([]models.ColumnLinkable{colLink})

				cTypes := models.NewContentType()
				cTypes.SetId(&cTypeID)
				cTypesCol := models.NewContentTypeCollectionResponse()
				cTypesCol.SetValue([]models.ContentTypeable{cTypes})

				listItem := models.NewListItem()
				listItem.SetId(&listItemID)
				listItemCol := models.NewListItemCollectionResponse()
				listItemCol.SetValue([]models.ListItemable{listItem})

				fields := models.NewFieldValueSet()
				fieldsData := map[string]any{
					"itemName": "item1",
					"itemSize": 4,
				}
				fields.SetAdditionalData(fieldsData)

				interceptV1Path(
					"sites", siteID,
					"lists", listID).
					Reply(200).
					JSON(graphTD.ParseableToMap(suite.T(), list))

				interceptV1Path(
					"sites", siteID,
					"lists", listID,
					"columns").
					Reply(200).
					JSON(graphTD.ParseableToMap(suite.T(), columnDefCol))

				interceptV1Path(
					"sites", siteID,
					"lists", listID,
					"items").
					Reply(200).
					JSON(graphTD.ParseableToMap(suite.T(), listItemCol))

				interceptV1Path(
					"sites", siteID,
					"lists", listID,
					"items", listItemID,
					"fields").
					Reply(200).
					JSON(graphTD.ParseableToMap(suite.T(), fields))

				interceptV1Path(
					"sites", siteID,
					"lists", listID,
					"contentTypes").
					Reply(200).
					JSON(graphTD.ParseableToMap(suite.T(), cTypesCol))

				interceptV1Path(
					"sites", siteID,
					"lists", listID,
					"contentTypes", cTypeID,
					"columns").
					Reply(200).
					JSON(graphTD.ParseableToMap(suite.T(), columnDefCol2))

				interceptV1Path(
					"sites", siteID,
					"lists", listID,
					"contentTypes", cTypeID,
					"columnLinks").
					Reply(200).
					JSON(graphTD.ParseableToMap(suite.T(), colLinkCol))
			},
			expect: assert.NoError,
		},
	}

	for _, test := range tests {
		suite.Run(test.name, func() {
			t := suite.T()

			ctx, flush := tester.NewContext(t)
			defer flush()

			defer gock.Off()
			test.setupf()

			list, info, err := suite.its.gockAC.Lists().GetListByID(ctx, siteID, listID)
			test.expect(t, err)
			assert.Equal(t, listID, *list.GetId())

			items := list.GetItems()
			assert.Equal(t, 1, len(items))
			assert.Equal(t, listItemID, *items[0].GetId())

			expectedItemData := map[string]any{"itemName": ptr.To[string]("item1"), "itemSize": ptr.To[float64](float64(4))}
			itemFields := items[0].GetFields()
			itemData := itemFields.GetAdditionalData()
			assert.Equal(t, expectedItemData, itemData)

			columns := list.GetColumns()
			assert.Equal(t, 1, len(columns))
			assert.Equal(t, textColumnDefID, *columns[0].GetId())

			cTypes := list.GetContentTypes()
			assert.Equal(t, 1, len(cTypes))
			assert.Equal(t, cTypeID, *cTypes[0].GetId())

			colLinks := cTypes[0].GetColumnLinks()
			assert.Equal(t, 1, len(colLinks))
			assert.Equal(t, colLinkID, *colLinks[0].GetId())

			columns = cTypes[0].GetColumns()
			assert.Equal(t, 1, len(columns))
			assert.Equal(t, numColumnDefID, *columns[0].GetId())

			assert.Equal(t, listName, info.List.Name)
			assert.Equal(t, int64(1), info.List.ItemCount)
			assert.Equal(t, listTemplate, info.List.Template)
			assert.NotEmpty(t, info.Modified)
			assert.NotEmpty(t, info.Created)
		})
	}
}

func (suite *ListsAPIIntgSuite) TestLists_PostList() {
	t := suite.T()

	ctx, flush := tester.NewContext(t)
	defer flush()

	var (
		acl      = suite.its.ac.Lists()
		siteID   = suite.its.site.id
		listName = testdata.DefaultRestoreConfig("list_api_post_list").Location
	)

	writer := kjson.NewJsonSerializationWriter()
	defer writer.Close()

	fieldsData, list := getFieldsDataAndList()

	newList, err := acl.PostList(ctx, siteID, listName, list, fault.New(true))
	require.NoError(t, err, clues.ToCore(err))
	assert.Equal(t, listName, ptr.Val(newList.GetDisplayName()))

	_, err = acl.PostList(ctx, siteID, listName, list, fault.New(true))
	require.Error(t, err)

	newListItems := newList.GetItems()
	require.Less(t, 0, len(newListItems))

	newListItemFields := newListItems[0].GetFields()
	require.NotEmpty(t, newListItemFields)

	newListItemsData := newListItemFields.GetAdditionalData()
	require.NotEmpty(t, newListItemsData)
	assert.Equal(t, fieldsData, newListItemsData)

	err = acl.DeleteList(ctx, siteID, ptr.Val(newList.GetId()))
	require.NoError(t, err)
}

func (suite *ListsAPIIntgSuite) TestLists_PostList_invalidTemplate() {
	t := suite.T()

	ctx, flush := tester.NewContext(t)
	defer flush()

	var (
		acl      = suite.its.ac.Lists()
		siteID   = suite.its.site.id
		listName = testdata.DefaultRestoreConfig("list_api_post_list").Location
	)

	tests := []struct {
		name     string
		template string
		expect   assert.ErrorAssertionFunc
	}{
		{
			name:     "list with template documentLibrary",
			template: DocumentLibraryListTemplate,
			expect:   assert.Error,
		},
		{
			name:     "list with template webTemplateExtensionsList",
			template: WebTemplateExtensionsListTemplate,
			expect:   assert.Error,
		},
		{
			name:     "list with template sharingLinks",
			template: SharingLinksListTemplate,
			expect:   assert.Error,
		},
		{
			name:     "list with template accessRequest",
			template: AccessRequestsListTemplate,
			expect:   assert.Error,
		},
	}

	for _, test := range tests {
		suite.Run(test.name, func() {
			t := suite.T()

			overrideListInfo := models.NewListInfo()
			overrideListInfo.SetTemplate(ptr.To(test.template))

			_, list := getFieldsDataAndList()
			list.SetList(overrideListInfo)

			_, err := acl.PostList(
				ctx,
				siteID,
				listName,
				list,
				fault.New(false))
			require.Error(t, err)
			assert.Equal(t, ErrSkippableListTemplate.Error(), err.Error())
		})
	}
}

func (suite *ListsAPIIntgSuite) TestLists_PatchList() {
	t := suite.T()

	ctx, flush := tester.NewContext(t)
	defer flush()

	var (
		acl         = suite.its.ac.Lists()
		siteID      = suite.its.site.id
		listName    = "old-list-name"
		newListName = "new-list-name"
	)

	fieldsData, list := getFieldsDataAndList()

	createdList, err := acl.PostList(ctx, siteID, listName, list, fault.New(true))
	require.NoError(t, err, clues.ToCore(err))
	assert.Equal(t, listName, ptr.Val(createdList.GetDisplayName()))

	listID := ptr.Val(createdList.GetId())

	newList := models.NewList()
	newList.SetDisplayName(ptr.To(newListName))
	patchedList, err := acl.PatchList(ctx, siteID, listID, newList)
	require.NoError(t, err)
	assert.Equal(t, newListName, ptr.Val(patchedList.GetDisplayName()))

	patchedList, _, err = acl.GetListByID(ctx, siteID, listID)
	require.NoError(t, err)

	newListItems := patchedList.GetItems()
	require.Less(t, 0, len(newListItems))

	newListItemFields := newListItems[0].GetFields()
	require.NotEmpty(t, newListItemFields)

	newListItemsData := newListItemFields.GetAdditionalData()
	require.NotEmpty(t, newListItemsData)
	assert.Equal(t, fieldsData["itemName"], newListItemsData["itemName"])

	err = acl.DeleteList(ctx, siteID, ptr.Val(patchedList.GetId()))
	require.NoError(t, err)
}

func (suite *ListsAPIIntgSuite) TestLists_DeleteList() {
	t := suite.T()

	ctx, flush := tester.NewContext(t)
	defer flush()

	var (
		acl      = suite.its.ac.Lists()
		siteID   = suite.its.site.id
		listName = testdata.DefaultRestoreConfig("list_api_post_list").Location
	)

	writer := kjson.NewJsonSerializationWriter()
	defer writer.Close()

	_, list := getFieldsDataAndList()

	newList, err := acl.PostList(ctx, siteID, listName, list, fault.New(true))
	require.NoError(t, err, clues.ToCore(err))
	assert.Equal(t, listName, ptr.Val(newList.GetDisplayName()))

	err = acl.DeleteList(ctx, siteID, ptr.Val(newList.GetId()))
	require.NoError(t, err)
}

func getFieldsDataAndList() (map[string]any, *models.List) {
	oldListID := "old-list"
	listItemID := "list-item1"
	textColumnDefID := "list-col1"
	textColumnDefName := "itemName"
	template := "genericList"

	listInfo := models.NewListInfo()
	listInfo.SetTemplate(&template)

	textColumn := models.NewTextColumn()

	txtColumnDef := models.NewColumnDefinition()
	txtColumnDef.SetId(&textColumnDefID)
	txtColumnDef.SetName(&textColumnDefName)
	txtColumnDef.SetText(textColumn)

	fields := models.NewFieldValueSet()
	fieldsData := map[string]any{
		textColumnDefName: ptr.To("item1"),
	}
	fields.SetAdditionalData(fieldsData)

	listItem := models.NewListItem()
	listItem.SetId(&listItemID)
	listItem.SetFields(fields)

	list := models.NewList()
	list.SetId(&oldListID)
	list.SetList(listInfo)
	list.SetColumns([]models.ColumnDefinitionable{txtColumnDef})
	list.SetItems([]models.ListItemable{listItem})

	return fieldsData, list
}
