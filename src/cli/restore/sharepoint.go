package restore

import (
	"github.com/spf13/cobra"

	"github.com/alcionai/corso/src/cli/flags"
	"github.com/alcionai/corso/src/cli/utils"
	"github.com/alcionai/corso/src/pkg/dttm"
	"github.com/alcionai/corso/src/pkg/selectors"
)

// called by restore.go to map subcommands to provider-specific handling.
func addSharePointCommands(cmd *cobra.Command) *cobra.Command {
	var c *cobra.Command

	switch cmd.Use {
	case restoreCommand:
		c, _ = utils.AddCommand(cmd, sharePointRestoreCmd())

		c.Use = c.Use + " " + sharePointServiceCommandUseSuffix

		flags.AddBackupIDFlag(c, true)
		flags.AddSharePointDetailsAndRestoreFlags(c)
		flags.AddNoPermissionsFlag(c)
		flags.AddRestoreConfigFlags(c, true)
		flags.AddFailFastFlag(c)
	}

	return c
}

const (
	sharePointServiceCommand          = "sharepoint"
	sharePointServiceCommandUseSuffix = "--backup <backupId>"

	//nolint:lll
	sharePointServiceCommandRestoreExamples = `# Restore file with ID 98765abcdef in Bob's latest backup (1234abcd...)
corso restore sharepoint --backup 1234abcd-12ab-cd34-56de-1234abcd --file 98765abcdef

# Restore the file with ID 98765abcdef without its associated permissions
corso restore sharepoint --backup 1234abcd-12ab-cd34-56de-1234abcd \
    --file 98765abcdef --no-permissions

# Restore files named "ServerRenderTemplate.xsl" in the folder "Display Templates/Style Sheets".
corso restore sharepoint --backup 1234abcd-12ab-cd34-56de-1234abcd \
    --file "ServerRenderTemplate.xsl" --folder "Display Templates/Style Sheets"

# Restore all files in the folder "Display Templates/Style Sheets" that were created before 2020.
corso restore sharepoint --backup 1234abcd-12ab-cd34-56de-1234abcd \
    --file-created-before 2020-01-01T00:00:00 --folder "Display Templates/Style Sheets"

# Restore all files in the "Documents" library.
corso restore sharepoint --backup 1234abcd-12ab-cd34-56de-1234abcd \
    --library Documents --folder "Display Templates/Style Sheets" `
)

// `corso restore sharepoint [<flag>...]`
func sharePointRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:     sharePointServiceCommand,
		Short:   "Restore M365 SharePoint service data",
		RunE:    restoreSharePointCmd,
		Args:    cobra.NoArgs,
		Example: sharePointServiceCommandRestoreExamples,
	}
}

// processes an sharepoint service restore.
func restoreSharePointCmd(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	if utils.HasNoFlagsAndShownHelp(cmd) {
		return nil
	}

	opts := utils.MakeSharePointOpts(cmd)
	opts.RestoreCfg.DTTMFormat = dttm.HumanReadableDriveItem

	if flags.RunModeFV == flags.RunModeFlagTest {
		return nil
	}

	if err := utils.ValidateSharePointRestoreFlags(flags.BackupIDFV, opts); err != nil {
		return err
	}

	sel := utils.IncludeSharePointRestoreDataSelectors(ctx, opts)
	utils.FilterSharePointRestoreInfoSelectors(sel, opts)

	// Exclude lists from restore since they are not supported yet.
	sel.Exclude(sel.Lists(selectors.Any()))

	return runRestore(
		ctx,
		cmd,
		opts.RestoreCfg,
		sel.Selector,
		flags.BackupIDFV,
		"SharePoint")
}
