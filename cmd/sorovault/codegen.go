package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/codegen"
)

func newCodegenCmd() *cobra.Command {
	var (
		network     string
		wasmHash    string
		lang        string
		output      string
		withZod     bool
		asPackage   bool
		packageName string
		pkgVersion  string
	)

	cmd := &cobra.Command{
		Use:   "codegen <contract_id>",
		Short: "Generate a typed client for a registered contract",
		Long: `Generate a typed client from a registered contract's stored interface.

The client is generated from what SoroVault already holds, so no network
access is needed. --wasm-hash generates against a superseded version.

Supported formats are TypeScript (ts), Markdown (md) and example argument
lists (examples): one example argument object per function, keyed by
function name. With --package the output is a complete npm package: the
client under src/, a package.json that pins @stellar/stellar-sdk and buffer
as peer dependencies, a tsconfig.json and a README, ready to publish or
install from a tarball.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if lang != "ts" && lang != "typescript" && lang != "md" && lang != "markdown" && lang != "examples" {
				return fmt.Errorf("unsupported --lang %q (supported: ts, md, examples)", lang)
			}
			if asPackage {
				if lang == "md" || lang == "markdown" {
					return fmt.Errorf("--package writes a TypeScript package, so --lang md cannot be packaged")
				}
				if lang == "examples" {
					return fmt.Errorf("--package writes a TypeScript package, so --lang examples cannot be packaged")
				}
				if strings.TrimSpace(packageName) == "" {
					return fmt.Errorf("--package needs --name: the npm package name to publish")
				}
			} else if pkgVersion != "" {
				return fmt.Errorf("--package-version only applies to --package")
			}
			if withZod && lang != "ts" && lang != "typescript" {
				return fmt.Errorf("--zod generates TypeScript schemas, so it needs --lang ts")
			}

			return withApp(cmd.Context(), func(ctx context.Context, a *app) error {
				net := network
				if net == "" {
					var err error
					if net, err = a.registry.Network(ctx); err != nil {
						return err
					}
				}

				stored, err := a.registry.Store().GetSpec(ctx, net, args[0], wasmHash)
				if err != nil {
					return err
				}

				src := codegen.Source{
					ContractID: stored.ContractID,
					Network:    stored.Network,
					WasmHash:   stored.WasmHash,
					WithZod:    withZod,
				}

				if asPackage {
					files, err := codegen.Package(stored.Interface, src, codegen.PackageOptions{
						Name:    packageName,
						Version: pkgVersion,
						WithZod: withZod,
					})
					if err != nil {
						return err
					}

					dir := output
					if dir == "" || dir == "-" {
						dir = packageDirName(packageName)
					}
					for _, file := range files {
						path := filepath.Join(dir, filepath.FromSlash(file.Path))
						if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
							return err
						}
						if err := os.WriteFile(path, file.Data, 0o600); err != nil {
							return err
						}
					}
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s (%d files)\n", dir, len(files))
					return nil
				}

				var source string
				switch lang {
				case "md", "markdown":
					source, err = codegen.Markdown(stored.Interface, src)
				case "examples":
					var body []byte
					body, err = codegen.Examples(stored.Interface, src)
					source = string(body)
				default:
					source, err = codegen.TypeScript(stored.Interface, src)
				}
				if err != nil {
					return err
				}

				if output == "" || output == "-" {
					_, err = io.WriteString(cmd.OutOrStdout(), source)
					return err
				}
				if err := os.WriteFile(output, []byte(source), 0o600); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s\n", output)
				return nil
			})
		},
	}

	cmd.Flags().StringVar(&network, "network", "", "network to read from (default: the network RPC_URL serves)")
	cmd.Flags().StringVar(&wasmHash, "wasm-hash", "", "generate for a specific version instead of the current one")
	cmd.Flags().StringVar(&lang, "lang", "ts", "output format (ts, md or examples)")
	cmd.Flags().BoolVar(&withZod, "zod", false, "append Zod schemas to the TypeScript output (ts only)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "write to this file, or this directory with --package")
	cmd.Flags().BoolVar(&asPackage, "package", false, "write a complete npm package instead of one file")
	cmd.Flags().StringVar(&packageName, "name", "", "npm package name for --package (@scope/name)")
	cmd.Flags().StringVar(&pkgVersion, "package-version", "", "version for --package (default: derived from the WASM hash)")
	registerContractIDCompletion(cmd)
	registerNetworkCompletion(cmd)
	return cmd
}

// packageDirName is the directory --package writes to when --output is not
// given. A scope is part of the package name, not a directory to create, so
// @acme/zkvote becomes zkvote.
func packageDirName(name string) string {
	if _, base, ok := strings.Cut(name, "/"); ok {
		return base
	}
	return name
}
