package repo_test

import (
	"os"
	"testing"

	"github.com/alcionai/clues"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/alcionai/corso/src/cli"
	"github.com/alcionai/corso/src/cli/flags"
	cliTD "github.com/alcionai/corso/src/cli/testdata"
	"github.com/alcionai/corso/src/internal/tester"
	"github.com/alcionai/corso/src/internal/tester/tconfig"
	"github.com/alcionai/corso/src/pkg/account"
	"github.com/alcionai/corso/src/pkg/config"
	"github.com/alcionai/corso/src/pkg/control"
	"github.com/alcionai/corso/src/pkg/repository"
	"github.com/alcionai/corso/src/pkg/storage"
	storeTD "github.com/alcionai/corso/src/pkg/storage/testdata"
)

type FilesystemE2ESuite struct {
	tester.Suite
}

func TestFilesystemE2ESuite(t *testing.T) {
	suite.Run(t, &FilesystemE2ESuite{Suite: tester.NewE2ESuite(
		t,
		[][]string{storeTD.AWSStorageCredEnvs, tconfig.M365AcctCredEnvs})})
}

func (suite *FilesystemE2ESuite) TestInitFilesystemCmd() {
	table := []struct {
		name          string
		hasConfigFile bool
	}{
		{
			name:          "NoConfigFile",
			hasConfigFile: false,
		},
		{
			name:          "hasConfigFile",
			hasConfigFile: true,
		},
	}

	for _, test := range table {
		suite.Run(test.name, func() {
			t := suite.T()

			ctx, flush := tester.NewContext(t)
			defer flush()

			st := storeTD.NewFilesystemStorage(t)

			cfg, err := st.ToFilesystemConfig()
			require.NoError(t, err, clues.ToCore(err))

			force := map[string]string{
				tconfig.TestCfgStorageProvider: storage.ProviderFilesystem.String(),
			}

			vpr, configFP := tconfig.MakeTempTestConfigClone(t, force)
			if !test.hasConfigFile {
				// Ideally we could use `/dev/null`, but you need a
				// toml file plus this works cross platform
				os.Remove(configFP)
			}

			ctx = config.SetViper(ctx, vpr)

			cmd := cliTD.StubRootCmd(
				"repo", "init", "filesystem",
				"--"+flags.ConfigFileFN, configFP,
				"--path", cfg.Path)
			cli.BuildCommandTree(cmd)

			// run the command
			err = cmd.ExecuteContext(ctx)
			require.NoError(t, err, clues.ToCore(err))

			// a second initialization should result in an error
			err = cmd.ExecuteContext(ctx)
			assert.ErrorIs(t, err, repository.ErrorRepoAlreadyExists, clues.ToCore(err))
		})
	}
}

func (suite *FilesystemE2ESuite) TestConnectFilesystemCmd() {
	table := []struct {
		name          string
		hasConfigFile bool
	}{
		{
			name:          "NoConfigFile",
			hasConfigFile: false,
		},
		{
			name:          "HasConfigFile",
			hasConfigFile: true,
		},
	}

	for _, test := range table {
		suite.Run(test.name, func() {
			t := suite.T()

			ctx, flush := tester.NewContext(t)
			defer flush()

			st := storeTD.NewFilesystemStorage(t)
			cfg, err := st.ToFilesystemConfig()
			require.NoError(t, err, clues.ToCore(err))

			force := map[string]string{
				tconfig.TestCfgAccountProvider: account.ProviderM365.String(),
				tconfig.TestCfgStorageProvider: storage.ProviderFilesystem.String(),
				tconfig.TestCfgFilesystemPath:  cfg.Path,
			}
			vpr, configFP := tconfig.MakeTempTestConfigClone(t, force)
			if !test.hasConfigFile {
				// Ideally we could use `/dev/null`, but you need a
				// toml file plus this works cross platform
				os.Remove(configFP)
			}

			ctx = config.SetViper(ctx, vpr)

			// init the repo first
			r, err := repository.New(
				ctx,
				tconfig.NewM365Account(t),
				st,
				control.DefaultOptions(),
				repository.NewRepoID)
			require.NoError(t, err, clues.ToCore(err))

			err = r.Initialize(ctx, repository.InitConfig{})
			require.NoError(t, err, clues.ToCore(err))

			// then test it
			cmd := cliTD.StubRootCmd(
				"repo", "connect", "filesystem",
				"--"+flags.ConfigFileFN, configFP,
				"--path", cfg.Path)
			cli.BuildCommandTree(cmd)

			// run the command
			err = cmd.ExecuteContext(ctx)
			require.NoError(t, err, clues.ToCore(err))
		})
	}
}
