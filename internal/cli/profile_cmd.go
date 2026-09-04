package cli

import (
	"errors"
	"fmt"
	"regexp"
	"teleparse/internal/config"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

// profileNamePattern constrains profile names to safe TOML keys and paths.
var profileNamePattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// configFilters aliases the filter-options surface for profile storage.
type configFilters = config.Filters

var errProfileName = errors.New("profile name must match [a-z0-9_-]{1,32}")

// validProfileName rejects unsafe profile names.
func validProfileName(name string) error {
	if !profileNamePattern.MatchString(name) {
		return fmt.Errorf("%q: %w", name, errProfileName)
	}

	return nil
}

// configPath resolves the effective config file path for a command.
func configPath(cmd *cobra.Command) string {
	if path, err := cmd.Flags().GetString("config"); err == nil && path != "" {
		return path
	}

	return config.DefaultConfigPath()
}

func profileCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage named filter profiles stored in config.toml",
	}
	cmd.AddCommand(
		profileSaveCmd(app),
		profileListCmd(app),
		profileShowCmd(app),
		profileRmCmd(app),
	)

	return cmd
}

func profileSaveCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "save NAME",
		Short:   "Save current [filters] defaults as named profile",
		Example: "  teleparse profile save photos-only",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validProfileName(name); err != nil {
				return fail(cmd, err)
			}
			if app.cfg.Profiles == nil {
				app.cfg.Profiles = map[string]configFilters{}
			}
			app.cfg.Profiles[name] = app.cfg.Filters

			if err := app.cfg.Save(configPath(cmd)); err != nil {
				return fail(cmd, fmt.Errorf("save config: %w", err))
			}

			return printLine(cmd, "saved profile %q", name)
		},
	}
}

func profileListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List profiles",
		Example: "  teleparse profile list",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(app.cfg.Profiles) == 0 {
				return printLine(cmd, "no profiles — create one: teleparse profile save NAME")
			}
			for name := range app.cfg.Profiles {
				if err := printLine(cmd, "%s", name); err != nil {
					return fail(cmd, fmt.Errorf("print profile list: %w", err))
				}
			}

			return nil
		},
	}
}

func profileShowCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "show NAME",
		Short:   "Show profile filters",
		Example: "  teleparse profile show photos-only",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prof, err := app.cfg.Profile(args[0])
			if err != nil {
				return fail(cmd, err)
			}
			raw, err := tomlMarshal(prof)
			if err != nil {
				return fail(cmd, fmt.Errorf("render profile: %w", err))
			}

			return printLine(cmd, "%s", raw)
		},
	}
}

func profileRmCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "rm NAME",
		Short:   "Remove profile",
		Example: "  teleparse profile rm photos-only",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if _, ok := app.cfg.Profiles[name]; !ok {
				return fail(cmd, fmt.Errorf("profile %q: %w", name, config.ErrProfileAbsent))
			}
			delete(app.cfg.Profiles, name)

			if err := app.cfg.Save(configPath(cmd)); err != nil {
				return fail(cmd, fmt.Errorf("save config: %w", err))
			}

			return printLine(cmd, "removed profile %q", name)
		},
	}
}

// tomlMarshal renders v as TOML for profile display.
func tomlMarshal(v any) ([]byte, error) {
	raw, err := toml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("toml marshal: %w", err)
	}

	return raw, nil
}
