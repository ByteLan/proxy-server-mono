// Package updatecmd provides the shared, explicit update command for both CLIs.
package updatecmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"proxy-mono/internal/selfupdate"
	"proxy-mono/internal/version"
	"strings"
)

func Run(ctx context.Context, product string, args []string, output io.Writer) error {
	flags := flag.NewFlagSet(product+" update", flag.ContinueOnError)
	flags.SetOutput(output)
	opts := selfupdate.Options{CurrentVersion: version.Version}
	flags.StringVar(&opts.Repository, "repo", "ByteLan/proxy-server-mono", "GitHub repository owner/name")
	flags.StringVar(&opts.Channel, "channel", "main", "release channel: main or stable")
	flags.StringVar(&opts.Version, "version", "", "exact release tag (overrides channel)")
	flags.StringVar(&opts.Target, "target", "", "existing binary to replace (default: current executable)")
	flags.BoolVar(&opts.CheckOnly, "check", false, "check for a release without downloading/installing a binary")
	tokenFile := flags.String("token-file", "", "private repository token file (regular file, mode 0600)")
	flags.Usage = func() {
		fmt.Fprintf(output, "Usage: %s update [options]\nDownload a GitHub Release, verify SHA256, and replace the binary.\nGITHUB_TOKEN / GH_TOKEN are accepted for private repositories.\nConfiguration, databases and Hysteria/Xray cores are preserved.\nRun with write access to the target; restart the service after updating.\n", product)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments (use update --help)")
	}
	if *tokenFile != "" {
		token, err := readTokenFile(*tokenFile)
		if err != nil {
			return err
		}
		opts.Token = token
	}
	// An explicit target may be a different installed build; this CLI cannot infer
	// that file's version from its own linker metadata. Do not skip its update.
	if opts.Target != "" {
		opts.CurrentVersion = ""
	}
	return selfupdate.Run(ctx, product, opts, output)
}

func readTokenFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("read GitHub token file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("GitHub token file must be a regular private file (chmod 600)")
	}
	if info.Size() > 4096 {
		return "", errors.New("GitHub token file exceeds 4096 bytes")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open GitHub token file: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return "", errors.New("cannot read GitHub token file")
	}
	if len(b) > 4096 {
		return "", errors.New("GitHub token file exceeds 4096 bytes")
	}
	token := strings.TrimSpace(string(b))
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("GitHub token file must contain one nonempty token")
	}
	return token, nil
}
