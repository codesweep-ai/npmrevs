package cli

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/codesweep-ai/npmrevs"
)

func manualCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "manual",
		Short: "Print the manual",
		Long: "Print MANUAL.md, which is compiled into the binary. A machine with the tool\n" +
			"has the reference, with no checkout and no network.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, err := io.WriteString(a.stdout, npmrevs.ManualMD)
			return err
		},
	}
}
