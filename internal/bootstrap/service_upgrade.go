// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Upgrade reuses the installer's release verification and staging. It does not
// call setup's unit installer: an update must preserve the user's boot policy.
func upgradeService(f serviceOptions) error {
	manager, err := serviceManager(f)
	if err != nil {
		return err
	}
	if manager == "Homebrew" {
		if output, err := serviceCommand("brew", "upgrade", "knowledge"); err != nil {
			return fmt.Errorf("brew upgrade knowledge: %v: %s", err, output)
		}
		if err := stopService(f); err != nil {
			return err
		}
		return startService(f)
	}
	goos, goarch, err := detectPlatform()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	release, err := fetchRelease(ctx, githubAPIBaseURL, "", true)
	if err != nil {
		return err
	}
	client, server, err := serviceBinaries(f)
	if err != nil {
		return err
	}
	installed, err := installedServiceVersion(client, f.httpPort)
	if err != nil {
		return err
	}
	if cmp, ok := compareReleaseVersions(release.TagName, installed); ok && cmp < 0 {
		return fmt.Errorf("latest release %s is older than installed %s", release.TagName, installed)
	}
	if resolved, err := filepath.EvalSymlinks(client); err == nil {
		client = resolved
	}
	if resolved, err := filepath.EvalSymlinks(server); err == nil {
		server = resolved
	}
	clientDir, clientName := filepath.Dir(client), filepath.Base(client)
	targets := []binaryTarget{{assetBase: "knowledge-server", destDir: filepath.Dir(server), writeName: filepath.Base(server)}, {assetBase: "knowledge", destDir: clientDir, writeName: clientName}}
	staged, err := stageServiceRelease(ctx, release, goos, goarch, targets)
	if err != nil {
		return err
	}
	defer func() { discardStaged(staged) }()
	// Downloads and checksum validation complete before agents are interrupted.
	if err := stopService(f); err != nil {
		return err
	}
	if err := commitServiceUpgrade(staged, goos); err != nil {
		return err
	}
	if err := startService(f); err != nil {
		return err
	}
	version, ok := probeServiceClient(f.httpPort)
	if !ok || version != release.TagName {
		return fmt.Errorf("updated client did not report expected version %s (reported %q)", release.TagName, version)
	}
	fmt.Fprintf(os.Stdout, "Knowledge upgraded to %s\n", release.TagName)
	return nil
}

func installedServiceVersion(client string, port int) (string, error) {
	if version, ok := probeServiceClient(port); ok {
		return version, nil
	}
	output, err := serviceCommand(client, "version", "--http-port", strconv.Itoa(port))
	if err != nil {
		return "", fmt.Errorf("read installed client version: %w", err)
	}
	for line := range strings.SplitSeq(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "knowledge" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("installed Knowledge did not report its version")
}

func commitServiceUpgrade(staged []stagedBinary, goos string) error {
	for i, item := range staged {
		if _, err := commitStaged(item, goos); err != nil {
			return fmt.Errorf("upgrade stopped after committing %d binaries; retry upgrade to finish: %w", i, err)
		}
	}
	return nil
}

func stageServiceRelease(ctx context.Context, release *releaseResponse, goos, goarch string, targets []binaryTarget) ([]stagedBinary, error) {
	staged := make([]stagedBinary, 0, len(targets))
	for _, target := range targets {
		payload, err := fetchAndExtractOne(ctx, release, goos, goarch, target.assetBase)
		if err != nil {
			discardStaged(staged)
			return nil, err
		}
		name := target.writeName
		if goos == "windows" {
			name = strings.TrimSuffix(name, filepath.Ext(name))
		}
		item, err := stageBinary(target.destDir, payload, goos, name)
		if err != nil {
			discardStaged(staged)
			return nil, err
		}
		staged = append(staged, item)
	}
	return staged, nil
}
