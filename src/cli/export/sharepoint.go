package export

import (
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/alcionai/corso/src/cli/flags"
	"github.com/alcionai/corso/src/cli/utils"
	"github.com/alcionai/corso/src/pkg/selectors"
)

// called by export.go to map subcommands to provider-specific handling.
func addSharePointCommands(cmd *cobra.Command) *cobra.Command {
	var c *cobra.Command

	switch cmd.Use {
	case exportCommand:
		c, _ = utils.AddCommand(cmd, sharePointExportCmd())

		c.Use = c.Use + " " + sharePointServiceCommandUseSuffix

		flags.AddBackupIDFlag(c, true)
		flags.AddSharePointDetailsAndRestoreFlags(c)
		flags.AddExportConfigFlags(c)
		flags.AddFailFastFlag(c)
	}

	return c
}

const (
	sharePointServiceCommand          = "sharepoint"
	sharePointServiceCommandUseSuffix = "<destination> --backup <backupId>"

	//nolint:lll
	sharePointServiceCommandExportExamples = `# Export file with ID 98765abcdef in Bob's latest backup (1234abcd...) to /my-exports
corso export sharepoint --backup 1234abcd-12ab-cd34-56de-1234abcd --file 98765abcdef my-exports

# Export file "ServerRenderTemplate.xsl" in "Display Templates/Style Sheets" as archive to the current directory
corso export sharepoint --backup 1234abcd-12ab-cd34-56de-1234abcd \
    --file "ServerRenderTemplate.xsl" --folder "Display Templates/Style Sheets" --archive .

# Export all files in the folder "Display Templates/Style Sheets" that were created before 2020 to /my-exports
corso export sharepoint --backup 1234abcd-12ab-cd34-56de-1234abcd \
    --file-created-before 2020-01-01T00:00:00 --folder "Display Templates/Style Sheets" my-exports

# Export all files in the "Documents" library to the current directory.
corso export sharepoint --backup 1234abcd-12ab-cd34-56de-1234abcd \
    --library Documents --folder "Display Templates/Style Sheets" .`
)

// `corso export sharepoint [<flag>...] <destination>`
func sharePointExportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   sharePointServiceCommand,
		Short: "Export M365 SharePoint service data",
		RunE:  exportSharePointCmd,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return errors.New("missing export destination")
			}

			return nil
		},
		Example: sharePointServiceCommandExportExamples,
	}
}

// processes an sharepoint service export.
func exportSharePointCmd(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	if utils.HasNoFlagsAndShownHelp(cmd) {
		return nil
	}

	opts := utils.MakeSharePointOpts(cmd)

	if flags.RunModeFV == flags.RunModeFlagTest {
		return nil
	}

	if err := utils.ValidateSharePointRestoreFlags(flags.BackupIDFV, opts); err != nil {
		return err
	}

	sel := utils.IncludeSharePointRestoreDataSelectors(ctx, opts)
	utils.FilterSharePointRestoreInfoSelectors(sel, opts)

	// Exclude lists from exports since they are not supported yet.
	sel.Exclude(sel.Lists(selectors.Any()))

	return runExport(
		ctx,
		cmd,
		args,
		opts.ExportCfg,
		sel.Selector,
		flags.BackupIDFV,
		"SharePoint",
		defaultAcceptedFormatTypes)
}
