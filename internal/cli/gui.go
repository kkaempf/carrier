package cli

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/suse/carrier/internal/web"
)

func init() {
	flags := CmdGui.Flags()
	flags.Int("port", 0, "(PORT) The port to listen on. Leave empty to auto-assign a random port")
	viper.BindPFlag("port", flags.Lookup("port"))
	viper.BindEnv("port", "PORT")

	viper.BindEnv("local-filesystem", "LOCAL_FILESYSTEM")
}

// CmdGui implements the carrier gui command
var CmdGui = &cobra.Command{
	Use:   "gui",
	Short: "starts the Graphical User Interface of Carrier",
	RunE: func(cmd *cobra.Command, args []string) error {
		return web.StartGui(viper.GetInt("port"), viper.GetBool("local-filesystem"))
	},
	SilenceErrors: true,
	SilenceUsage:  true,
}
