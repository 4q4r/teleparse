package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/4q4r/teleparse/internal/tg"

	"github.com/gotd/td/telegram"
	"github.com/spf13/cobra"
)

func chatsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chats",
		Short: "List and inspect accessible chats",
	}

	cmd.AddCommand(
		chatsListCmd(app),
		chatsShowCmd(app),
	)

	return cmd
}

func chatsListCmd(app *App) *cobra.Command {
	var (
		types        []string
		contactsOnly bool
		asJSON       bool
	)

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List dialogs (all chats this account can see)",
		Example: "  teleparse chats list --type channel,forum\n  teleparse chats list --json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			creds, err := app.resolveCreds()
			if err != nil {
				return fail(cmd, err)
			}

			wanted := map[string]bool{}
			for _, chatType := range types {
				wanted[strings.TrimSpace(chatType)] = true
			}

			if err := runConnectivityCheck(cmd, app); err != nil {
				return fail(cmd, err)
			}

			err = tg.Run(cmd.Context(), app.cfg.Auth.Account, creds, app.cfg, app.paths, func(
				ctx context.Context,
				client *telegram.Client,
			) error {
				chats, err := collectChats(ctx, client, contactsOnly)
				if err != nil {
					return fmt.Errorf("collect chats: %w", err)
				}

				format := app.outputFormat()
				if asJSON {
					format = FormatJSON
				}

				return renderChats(cmd, filterChatsByType(chats, wanted), format)
			})
			if err != nil {
				return fail(cmd, err)
			}

			return nil
		},
	}
	cmd.Flags().StringSliceVar(&types, "type", nil,
		"chat types: private,bot,group,supergroup,channel,forum (comma list)")
	cmd.Flags().BoolVar(&contactsOnly, "contacts-only", false, "list the account contacts instead of dialogs")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")

	return cmd
}

func chatsShowCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "show CHAT",
		Short:   "Show chat metadata",
		Example: "  teleparse chats show @durov\n  teleparse chats show 123456789",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			creds, err := app.resolveCreds()
			if err != nil {
				return fail(cmd, err)
			}

			ref := args[0]

			if ref == "saved" {
				return showSavedMessages(cmd, app, creds)
			}

			if err := runConnectivityCheck(cmd, app); err != nil {
				return fail(cmd, err)
			}

			err = tg.Run(cmd.Context(), app.cfg.Auth.Account, creds, app.cfg, app.paths, func(
				ctx context.Context,
				client *telegram.Client,
			) error {
				info, err := tg.FindChat(ctx, client, ref)
				if err != nil {
					return fmt.Errorf("find chat %q: %w", ref, err)
				}

				printChatDetail(cmd, *info)

				return nil
			})
			if err != nil {
				if errors.Is(err, tg.ErrChatNotFound) {
					return fail(cmd, fmt.Errorf("%w\n  run `teleparse chats list` to see the ids you can use", err))
				}

				return fail(cmd, err)
			}

			return nil
		},
	}
}

func showSavedMessages(cmd *cobra.Command, app *App, creds tg.Creds) error {
	manager := tg.NewAccountManager(app.paths.AccountsDir)

	storage, err := manager.Storage(app.cfg.Auth.Account)
	if err != nil {
		return fail(cmd, err)
	}

	if err := runConnectivityCheck(cmd, app); err != nil {
		return fail(cmd, err)
	}

	err = tg.Run(cmd.Context(), app.cfg.Auth.Account, creds, app.cfg, app.paths, func(
		ctx context.Context,
		client *telegram.Client,
	) error {
		info, err := tg.WhoAmI(ctx, client, storage)
		if err != nil {
			return fmt.Errorf("whoami: %w", err)
		}

		if !info.Authorized {
			return errNotAuthorized
		}

		printChatDetail(cmd, tg.ChatInfo{
			ID:       info.ID,
			Title:    "Saved Messages (you)",
			Type:     tg.ChatTypePrivate,
			Username: info.Username,
		})

		return nil
	})
	if errors.Is(err, errNotAuthorized) {
		return fail(cmd, fmt.Errorf("%w: run teleparse auth login", errNotAuthorized))
	}

	if err != nil {
		return fail(cmd, err)
	}

	return nil
}

func collectChats(ctx context.Context, client *telegram.Client, contactsOnly bool) ([]tg.ChatInfo, error) {
	if contactsOnly {
		chats, err := tg.Contacts(ctx, client)
		if err != nil {
			return nil, fmt.Errorf("list contacts: %w", err)
		}

		return chats, nil
	}

	chats, err := tg.Chats(ctx, client, "")
	if err != nil {
		return nil, fmt.Errorf("list dialogs: %w", err)
	}

	return chats, nil
}

func filterChatsByType(chats []tg.ChatInfo, wanted map[string]bool) []tg.ChatInfo {
	if len(wanted) == 0 {
		return chats
	}

	filtered := make([]tg.ChatInfo, 0, len(chats))

	for _, chat := range chats {
		if wanted[chat.Type] {
			filtered = append(filtered, chat)
		}
	}

	return filtered
}

// chatPlainKeys are the key: value column names of the plain chats render.
func chatPlainKeys() []string {
	return []string{"id", "type", "title", "username", "archived", "protected"}
}

// chatPlainRow renders one chat as a plain value row.
func chatPlainRow(chat tg.ChatInfo) []string {
	return []string{
		strconv.FormatInt(chat.ID, 10), chat.Type, chat.Title, chat.Username,
		strconv.FormatBool(chat.Archived), strconv.FormatBool(chat.Protected),
	}
}

// renderChats prints chats in the requested format; the local --json flag
// is an alias for --format json.
func renderChats(cmd *cobra.Command, chats []tg.ChatInfo, format OutputFormat) error {
	if format == FormatJSON {
		if len(chats) == 0 {
			chats = []tg.ChatInfo{}
		}

		return printJSON(cmd, chats)
	}

	if format == FormatPlain {
		if len(chats) == 0 {
			return printLine(cmd, "no chats\n")
		}

		rows := make([][]string, 0, len(chats))
		for _, chat := range chats {
			rows = append(rows, chatPlainRow(chat))
		}

		return printPlainRows(cmd, chatPlainKeys(), rows)
	}

	if len(chats) == 0 {
		return printLine(cmd, "no chats\n")
	}

	for _, chat := range chats {
		archived := ""
		if chat.Archived {
			archived = " [archived]"
		}

		protected := ""
		if chat.Protected {
			protected = " [protected]"
		}

		username := ""
		if chat.Username != "" {
			username = " @" + chat.Username
		}

		if err := printLine(cmd, "%d\t%s\t%s%s%s%s\n",
			chat.ID, chat.Type, chat.Title, username, archived, protected); err != nil {
			return err
		}
	}

	return nil
}

func printChatDetail(cmd *cobra.Command, chat tg.ChatInfo) {
	detail := fmt.Sprintf("id:       %d\ntitle:    %s\ntype:     %s\narchived:  %t\nprotected: %t\n",
		chat.ID, chat.Title, chat.Type, chat.Archived, chat.Protected)

	if chat.Username != "" {
		detail += fmt.Sprintf("username: @%s\n", chat.Username)
	}

	_ = printLine(cmd, "%s", detail)
}
