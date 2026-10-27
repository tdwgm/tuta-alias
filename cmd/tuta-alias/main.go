// tuta-alias — the Go rewrite. cobra dispatches to internal/cliflow for
// scripted commands; with no args, the root command launches the TUI.
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/tdwgm/tuta-alias/internal/api"
	"github.com/tdwgm/tuta-alias/internal/bw"
	"github.com/tdwgm/tuta-alias/internal/cliflow"
	"github.com/tdwgm/tuta-alias/internal/config"
	"github.com/tdwgm/tuta-alias/internal/consts"
	"github.com/tdwgm/tuta-alias/internal/legacy"
	"github.com/tdwgm/tuta-alias/internal/logx"
	"github.com/tdwgm/tuta-alias/internal/serve"
	"github.com/tdwgm/tuta-alias/internal/tui"
)

func main() {
	// Carry ~/.config/tuta-cli, ~/.cache/tuta-cli and the tmpfs dir over to
	// the tuta-alias names before anything reads them (0.10.0 rename).
	legacy.Migrate()
	legacy.RefreshComments(config.Path())
	if err := buildRoot().Execute(); err != nil {
		// SilenceErrors keeps cobra quiet so the proxy check can print its
		// own one-liner. Everything else — an unknown command, a missing
		// argument — was then swallowed and the program just exited 2 with
		// no output at all.
		var reported reportedError
		if !errors.As(err, &reported) {
			fmt.Fprintln(os.Stderr, "tuta-alias:", err)
		}
		logx.Errorf("exit: %v", err)
		os.Exit(2)
	}
}

// annNetwork marks commands that talk to Tuta: an unusable TUTA_PROXY stops
// them before they start. Only the executing command is checked — the root
// (TUI) is one, and inheriting it would also lock out `config edit`, the way
// to fix the value.
const annNetwork = "tuta-alias/network"

func network(c *cobra.Command) *cobra.Command {
	c.Annotations = map[string]string{annNetwork: "true"}
	return c
}

// applyProxy wires TUTA_PROXY into internal/api once, before any command runs.
// api fails closed on its own (an invalid or unknown proxy makes every request
// fail, never go direct); this turns that into an early one-line error for
// network commands while offline ones (config, options, plus, version) work.
func applyProxy(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load()
	if err != nil {
		err = fmt.Errorf("cannot read TUTA_PROXY: %w", err)
		api.FailClosed(err)
	} else {
		logx.Setup(cfg)
		if cmd.Name() != "logs" {
			// Reading the log must not add a line to it.
			logx.Debugf("run %s", cmd.CommandPath())
		}
		err = api.SetProxy(cfg.TutaProxy)
	}
	if err != nil && cmd.Annotations[annNetwork] == "true" {
		// SilenceErrors is on and main exits without printing: say it here.
		fmt.Fprintf(cmd.ErrOrStderr(), "tuta-alias: %v — fix it in %s (tuta-alias config edit)\n", err, config.Path())
		logx.Errorf("%s: %v", cmd.CommandPath(), err)
		return reportedError{err}
	}
	return nil
}

// reportedError marks an error whose message has already been printed, so main
// does not print it twice.
type reportedError struct{ err error }

func (e reportedError) Error() string { return e.err.Error() }
func (e reportedError) Unwrap() error { return e.err }

// servePortFromConfig takes SERVE_PORT when the flag was not given.
func servePortFromConfig(port *int, cfg *config.Config) {
	if n := cfg.PortInt(); n > 0 {
		*port = n
	}
}

// completeConfigKeys completes `config get/set` with the real key list, so
// shell completion and the field table cannot drift apart.
func completeConfigKeys(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := make([]string, 0, len(config.Fields))
	for _, f := range config.Fields {
		out = append(out, f.Key+"\t"+f.Description)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// versionLine is the version with the commit it was built from, when the
// build stamped one. The updater compares that commit with the ref's, so a
// user reporting a problem should be able to read it off `version`.
func versionLine() string {
	if c := consts.CommitHash(); c != "" {
		return consts.AppVersion() + " (" + c + ")"
	}
	return consts.AppVersion()
}

func buildRoot() *cobra.Command {
	var root *cobra.Command
	root = network(&cobra.Command{
		Use:               "tuta-alias",
		Short:             "Tuta Mail CLI — manage custom-domain aliases.",
		Version:           versionLine(),
		PersistentPreRunE: applyProxy,
		Run: func(cmd *cobra.Command, args []string) {
			// First-run: launch install wizard if config doesn't exist.
			if _, err := os.Stat(config.Path()); os.IsNotExist(err) {
				os.Exit(tui.RunInstallWizard(cmd.OutOrStdout(), cmd.ErrOrStderr(), root))
			}
			cfg, _ := config.Load()
			if cfg != nil && !cfg.TUIEnabled {
				_ = cmd.Help()
				return
			}
			os.Exit(tui.Run(cmd.OutOrStdout(), cmd.ErrOrStderr(), root))
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	})

	// login
	var (
		email   string
		totp    int
		verbose bool
		bwItem  string
		save    bool
		saved   bool
		auto    bool
	)
	loginCmd := network(&cobra.Command{
		Use:   "login",
		Short: "Log in and persist a session",
		Run: func(cmd *cobra.Command, args []string) {
			if auto {
				os.Exit(cliflow.AutoLogin(cmd.OutOrStdout(), cmd.ErrOrStderr(), os.Stdin, cliflow.LoginOpts{
					Email: email, TOTP: totp, Verbose: verbose, Save: save,
				}))
			}
			if bwItem != "" {
				if err := bw.ExecLogin(cmd.ErrOrStderr(), bwItem, email, totp, verbose); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), err)
					os.Exit(2)
				}
				return
			}
			os.Exit(cliflow.Login(cmd.OutOrStdout(), cmd.ErrOrStderr(), cliflow.LoginOpts{
				Email: email, TOTP: totp, Verbose: verbose, Save: save, Saved: saved,
			}))
		},
	})
	loginCmd.Flags().StringVar(&email, "email", "", "Account email (or $TUTA_EMAIL)")
	loginCmd.Flags().IntVar(&totp, "totp", 0, "TOTP code if 2FA is enabled")
	loginCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Print progress for each step")
	loginCmd.Flags().StringVar(&bwItem, "bw", "", "Auto-fill from this Bitwarden item")
	loginCmd.Flags().BoolVar(&save, "save", false, "Save email + password to the system keyring after login (never the TOTP)")
	loginCmd.Flags().BoolVar(&saved, "saved", false, "Use the email + password saved in the system keyring (prompts for TOTP)")
	loginCmd.Flags().StringVar(&bwItem, "bitwarden", "", "Alias for --bw")
	loginCmd.Flags().BoolVar(&auto, "auto", false, "Non-interactive: use the credential backend, else the saved keyring login; prints one JSON status line (exit 3 = need master password on stdin, 4 = need TOTP, 5 = need credentials)")
	root.AddCommand(loginCmd)

	root.AddCommand(network(&cobra.Command{
		Use:   "logout",
		Short: "Delete the persisted session file",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.Logout(cmd.OutOrStdout()))
		},
	}))

	var (
		logLines  int
		logPath   bool
		logClear  bool
		logRedact bool
	)
	logsCmd := &cobra.Command{
		Use:   "logs",
		Short: "Show the tail of the log file",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.Logs(cmd.OutOrStdout(), cmd.ErrOrStderr(), logLines, logPath, logClear, logRedact))
		},
	}
	logsCmd.Flags().IntVarP(&logLines, "lines", "n", 50, "How many lines to show")
	logsCmd.Flags().BoolVar(&logPath, "path", false, "Print the log file path and exit")
	logsCmd.Flags().BoolVar(&logClear, "clear", false, "Empty the log file")
	logsCmd.Flags().BoolVar(&logRedact, "redact", false, "Remove addresses, paths, user name and tokens (for sharing)")
	root.AddCommand(logsCmd)

	// vault — the built-in encrypted credential file.
	var vaultEmail string
	vaultCmd := &cobra.Command{
		Use:   "vault",
		Short: "Manage the built-in encrypted credential vault",
		Long: "The vault stores one login (email + password, never the TOTP) in an\n" +
			"argon2id + XChaCha20-Poly1305 file next to the config, for systems with\n" +
			"no Secret Service keyring. Select it with CRED_BACKEND=\"vault\".",
	}
	vaultSetCmd := &cobra.Command{
		Use:   "set",
		Short: "Create or replace the vault (prompts, never takes a secret in argv)",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.VaultSet(cmd.OutOrStdout(), cmd.ErrOrStderr(), os.Stdin, vaultEmail))
		},
	}
	vaultSetCmd.Flags().StringVar(&vaultEmail, "email", "", "Account email (asked for when not given)")
	vaultCmd.AddCommand(vaultSetCmd)
	vaultCmd.AddCommand(&cobra.Command{
		Use:   "passwd",
		Short: "Change the vault passphrase",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.VaultPasswd(cmd.OutOrStdout(), cmd.ErrOrStderr(), os.Stdin))
		},
	})
	var vaultYes bool
	vaultRmCmd := &cobra.Command{
		Use:   "rm",
		Short: "Delete the vault file",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.VaultRemove(cmd.OutOrStdout(), cmd.ErrOrStderr(), os.Stdin, vaultYes))
		},
	}
	vaultRmCmd.Flags().BoolVarP(&vaultYes, "yes", "y", false, "Do not ask for confirmation")
	vaultCmd.AddCommand(vaultRmCmd)
	vaultCmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show whether a vault exists and whether it is in use",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.VaultStatus(cmd.OutOrStdout(), cmd.ErrOrStderr()))
		},
	})
	root.AddCommand(vaultCmd)

	var updateCheck bool
	updateCmd := network(&cobra.Command{
		Use:   "update",
		Short: "Rebuild from UPDATE_REF and replace the installed binary",
		Long: "There is no release to download: an update is a rebuild from the branch\n" +
			"or tag in UPDATE_REF, using the git credentials and the Go toolchain this\n" +
			"machine already has. --check only reports (exit 1 when a newer build exists).",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.Update(cmd.OutOrStdout(), cmd.ErrOrStderr(), updateCheck))
		},
	})
	updateCmd.Flags().BoolVar(&updateCheck, "check", false, "Only report whether a newer build exists")
	root.AddCommand(updateCmd)

	var doctorJSON, doctorRedact bool
	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the installation, config, session and tooling",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.Doctor(cmd.OutOrStdout(), cmd.ErrOrStderr(), doctorJSON, doctorRedact))
		},
	}
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "Output JSON")
	doctorCmd.Flags().BoolVar(&doctorRedact, "redact", false, "Remove addresses, paths, user name and tokens (for sharing)")
	root.AddCommand(doctorCmd)

	root.AddCommand(network(&cobra.Command{
		Use:   "tui",
		Short: "Open the TUI (also what `tuta-alias` with no arguments does)",
		Run: func(cmd *cobra.Command, args []string) {
			// With TUI_ENABLED="false" the bare command prints help, which
			// left no way to open the TUI at all.
			os.Exit(tui.Run(cmd.OutOrStdout(), cmd.ErrOrStderr(), root))
		},
	}))

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the tuta-alias version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "tuta-alias %s\n%s\n", versionLine(), tui.LegalNotice)
		},
	})

	root.AddCommand(&cobra.Command{
		Use:   "whoami",
		Short: "Show the currently logged-in account",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.Whoami(cmd.OutOrStdout(), cmd.ErrOrStderr()))
		},
	})

	var asJSON bool
	domainsCmd := network(&cobra.Command{
		Use:   "domains",
		Short: "List custom domains on the account",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.Domains(cmd.OutOrStdout(), cmd.ErrOrStderr(),
				cliflow.ReadOpts{AsJSON: asJSON}))
		},
	})
	domainsCmd.Flags().BoolVar(&asJSON, "json", false, "Output JSON")
	root.AddCommand(domainsCmd)

	var dnsJSON bool
	dnsCmd := network(&cobra.Command{
		Use:   "dns-check <domain>",
		Short: "Ask Tuta to verify a custom domain's DNS records (like the wizard's refresh)",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.DNSCheck(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0],
				cliflow.ReadOpts{AsJSON: dnsJSON}))
		},
	})
	dnsCmd.Flags().BoolVar(&dnsJSON, "json", false, "Output JSON")
	root.AddCommand(dnsCmd)

	root.AddCommand(&cobra.Command{
		Use:   "verify-record <domain>",
		Short: "Print the t-verify TXT value Tuta expects for a domain",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.VerifyRecord(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0]))
		},
	})

	var aliasJSON bool
	var aliasDomain string
	aliasesCmd := network(&cobra.Command{
		Use:   "aliases",
		Short: "List email addresses grouped by domain",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.Aliases(cmd.OutOrStdout(), cmd.ErrOrStderr(),
				cliflow.ReadOpts{AsJSON: aliasJSON, DomainFilter: aliasDomain}))
		},
	})
	aliasesCmd.Flags().BoolVar(&aliasJSON, "json", false, "Output JSON")
	aliasesCmd.Flags().StringVar(&aliasDomain, "domain", "", "Filter by domain")
	root.AddCommand(aliasesCmd)

	root.AddCommand(network(&cobra.Command{
		Use:   "create USER@HOST",
		Short: "Create a new custom-domain alias",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.Create(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0]))
		},
	}))

	var restore bool
	deleteCmd := network(&cobra.Command{
		Use:     "delete USER@HOST",
		Aliases: []string{"remove", "rm"},
		Short:   "Disable or re-enable an alias",
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.Delete(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], restore))
		},
	})
	deleteCmd.Flags().BoolVar(&restore, "restore", false, "Re-enable a previously disabled alias")
	root.AddCommand(deleteCmd)

	var quotaJSON bool
	quotaCmd := network(&cobra.Command{
		Use:   "quota",
		Short: "Show quota for aliases",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.QuotaCmd(cmd.OutOrStdout(), cmd.ErrOrStderr(), quotaJSON))
		},
	})
	quotaCmd.Flags().BoolVar(&quotaJSON, "json", false, "Output JSON")
	root.AddCommand(quotaCmd)

	var (
		serveHost   string
		servePort   int
		printToken  bool
		rotateToken bool
	)
	serveCmd := network(&cobra.Command{
		Use:   "serve",
		Short: "Run a local REST API",
		Run: func(cmd *cobra.Command, args []string) {
			if printToken || rotateToken {
				get := serve.GetOrCreateToken
				if rotateToken {
					get = serve.RotateToken
				}
				tok, err := get()
				if err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), err)
					os.Exit(2)
				}
				if rotateToken {
					fmt.Fprintln(cmd.ErrOrStderr(), "token rotated — update your clients (a running serve reads the file on every request)")
				}
				fmt.Fprintln(cmd.OutOrStdout(), tok)
				return
			}
			// SERVE_HOST and SERVE_PORT were config keys the Config screen,
			// the install wizard and `options` all showed, and that nothing
			// read: serve used its own flag defaults. The flags now override
			// the config instead of ignoring it.
			cfg, err := config.Load()
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
				os.Exit(2)
			}
			if !cmd.Flags().Changed("host") {
				serveHost = cfg.ServeHost
			}
			if !cmd.Flags().Changed("port") {
				servePortFromConfig(&servePort, cfg)
			}
			if err := config.ValidateKey("SERVE_HOST", serveHost); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
				os.Exit(2)
			}
			// Before either banner names the token file, because with no HOME
			// and no XDG_CACHE_HOME there is nowhere to keep it: the path is
			// empty, both banners printed "Token file: " and the reason only
			// arrived after "Listening on". Run creates it too; by then this
			// has, so that call reads it.
			if _, err := serve.GetOrCreateToken(); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
				os.Exit(2)
			}
			if !serve.IsLoopback(serveHost) {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"Warning: binding to %s exposes this API beyond this machine.\n"+
						"         Anyone who can reach it and holds the token can create and delete aliases.\n"+
						"         Token file: %s\n\n", serveHost, serve.TokenPath())
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"Token file: %s\nListening on http://%s\nAuth: Authorization: Bearer <token>  (`tuta-alias serve --print-token`)\nStop: Ctrl-C\n",
				serve.TokenPath(), net.JoinHostPort(serveHost, strconv.Itoa(servePort)))
			if err := serve.Run(serveHost, servePort); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
				os.Exit(2)
			}
		},
	})
	serveCmd.Flags().StringVar(&serveHost, "host", "127.0.0.1", "Bind address (default: SERVE_HOST, else 127.0.0.1)")
	serveCmd.Flags().IntVar(&servePort, "port", 8088, "Port (default: SERVE_PORT, else 8088)")
	serveCmd.Flags().BoolVar(&printToken, "print-token", false, "Print the API token and exit")
	serveCmd.Flags().BoolVar(&rotateToken, "rotate-token", false, "Replace the API token with a new one and print it")
	root.AddCommand(serveCmd)

	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Run the TUI install wizard (or use --quiet for non-interactive)",
		Run: func(cmd *cobra.Command, args []string) {
			quiet, _ := cmd.Flags().GetBool("quiet")
			if quiet {
				os.Exit(cliflow.Install(cmd.OutOrStdout(), cmd.ErrOrStderr(), root))
			}
			os.Exit(tui.RunInstallWizard(cmd.OutOrStdout(), cmd.ErrOrStderr(), root))
		},
	}
	installCmd.Flags().Bool("quiet", false, "Non-interactive install (skip TUI wizard)")
	root.AddCommand(installCmd)

	var purge bool
	uninstallCmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the binary + completions and (with --purge) all local state",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.Uninstall(cmd.OutOrStdout(), cmd.ErrOrStderr(), purge))
		},
	}
	uninstallCmd.Flags().BoolVar(&purge, "purge", false,
		"Also delete the config directory (incl. saved + addresses and alias notes), session, api-token, client-version cache and keyring entries")
	root.AddCommand(uninstallCmd)

	// plus — generate/list local "+" (sub-)addresses (no API call).
	var (
		plusNote   string
		plusSuffix string
		plusJSON   bool
	)
	plusCmd := &cobra.Command{
		Use:   "plus BASE@DOMAIN",
		Short: "Generate a local + (sub-)address from a base address",
		Long: "Generate a + (sub-)address (e.g. spam+jv5d8@example.com) from a base\n" +
			"address and save it locally with an optional note. Sub-addressing\n" +
			"routes server-side, so no alias is created via the API.",
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.Plus(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], plusNote, plusSuffix))
		},
	}
	plusCmd.Flags().StringVar(&plusNote, "note", "", "Note for the address (e.g. where it's used)")
	plusCmd.Flags().StringVar(&plusSuffix, "suffix", "", "Use this suffix instead of an auto-generated one")
	plusListCmd := &cobra.Command{
		Use:   "list",
		Short: "List locally-saved + addresses",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.PlusList(cmd.OutOrStdout(), cmd.ErrOrStderr(), plusJSON))
		},
	}
	plusListCmd.Flags().BoolVar(&plusJSON, "json", false, "Output JSON")
	plusCmd.AddCommand(plusListCmd)
	root.AddCommand(plusCmd)

	var optionsJSON bool
	optionsCmd := &cobra.Command{
		Use:   "options",
		Short: "List all configuration options and current values",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.Options(cmd.OutOrStdout(), cmd.ErrOrStderr(), optionsJSON))
		},
	}
	optionsCmd.Flags().BoolVar(&optionsJSON, "json", false, "Output JSON")
	root.AddCommand(optionsCmd)

	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Show, get, set, check, edit or locate the config file",
	}
	configCmd.AddCommand(&cobra.Command{
		Use:   "show",
		Short: "Print the config file contents",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.ConfigShow(cmd.OutOrStdout(), cmd.ErrOrStderr()))
		},
	})
	configCmd.AddCommand(&cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.ConfigPath(cmd.OutOrStdout()))
		},
	})
	configCmd.AddCommand(&cobra.Command{
		Use:   "edit",
		Short: "Open the config file in $EDITOR",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.ConfigEdit(cmd.OutOrStdout(), cmd.ErrOrStderr()))
		},
	})
	configCmd.AddCommand(&cobra.Command{
		Use:   "get KEY",
		Short: "Print one config value",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.ConfigGet(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0]))
		},
		ValidArgsFunction: completeConfigKeys,
	})
	configCmd.AddCommand(&cobra.Command{
		Use:   "set KEY VALUE",
		Short: "Validate and store one config value",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.ConfigSet(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], args[1]))
		},
		ValidArgsFunction: completeConfigKeys,
	})
	configCmd.AddCommand(&cobra.Command{
		Use:   "check",
		Short: "Validate every value in the config file (exit 1 if something is wrong)",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(cliflow.ConfigCheck(cmd.OutOrStdout(), cmd.ErrOrStderr()))
		},
	})
	root.AddCommand(configCmd)

	return root
}
