package cli

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hiberus-industria/forge/internal/devflow"
	"github.com/hiberus-industria/forge/internal/platform/service"
)

const releasesAPI = "https://api.github.com/repos/hiberus-industria/forge/releases/latest"

type release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"url"` // API URL: works for private repos with Accept: octet-stream
	} `json:"assets"`
}

func updateCmd() *cobra.Command {
	var check bool
	c := &cobra.Command{
		Use:   "update",
		Short: "Actualiza forge a la última versión publicada",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			o := out{w: cmd.OutOrStdout()}
			token, _ := devflow.GitHubToken(ctx) // optional: needed if the repo is private
			rel, err := latestRelease(ctx, token)
			if err != nil {
				return err
			}
			latest := strings.TrimPrefix(rel.TagName, "v")
			if latest == strings.TrimPrefix(version, "v") {
				o.ok("Ya tienes la última versión (%s)", latest)
				return nil
			}
			o.info("Nueva versión disponible: %s → %s", sMuted.Render(version), sBold.Render(latest))
			if check {
				o.hint("%s", rel.HTMLURL)
				return nil
			}
			asset := fmt.Sprintf("forge_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
			archiveURL, sumsURL := "", ""
			for _, a := range rel.Assets {
				switch a.Name {
				case asset:
					archiveURL = a.URL
				case "checksums.txt":
					sumsURL = a.URL
				}
			}
			if archiveURL == "" || sumsURL == "" {
				return fmt.Errorf("la release %s no tiene %s o checksums.txt", rel.TagName, asset)
			}
			o.info("Descargando %s", asset)
			archive, err := download(ctx, archiveURL, token)
			if err != nil {
				return err
			}
			sums, err := download(ctx, sumsURL, token)
			if err != nil {
				return err
			}
			if err := verifyChecksum(archive, sums, asset); err != nil {
				return err
			}
			o.ok("Checksum SHA-256 verificado")
			bin, err := extractBinary(archive, "forge")
			if err != nil {
				return err
			}
			exe, err := executable()
			if err != nil {
				return err
			}
			if err := replaceExecutable(exe, bin); err != nil {
				return fmt.Errorf("replace %s: %w", exe, err)
			}
			o.ok("forge actualizado a %s", sBold.Render(latest))
			if st, _ := service.Get(); st.Installed {
				if _, err := service.Install(exe); err == nil {
					o.ok("Daemon reiniciado con la nueva versión")
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&check, "check", false, "solo comprobar si hay una versión nueva")
	return c
}

func githubRequest(ctx context.Context, url, token, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && token == "" {
			return nil, errors.New("release no encontrada (¿repositorio privado? ejecuta `gh auth login`)")
		}
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp, nil
}

func latestRelease(ctx context.Context, token string) (release, error) {
	var r release
	resp, err := githubRequest(ctx, releasesAPI, token, "application/vnd.github+json")
	if err != nil {
		return r, err
	}
	defer resp.Body.Close()
	return r, json.NewDecoder(resp.Body).Decode(&r)
}

func download(ctx context.Context, url, token string) ([]byte, error) {
	resp, err := githubRequest(ctx, url, token, "application/octet-stream")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

func verifyChecksum(data, sums []byte, name string) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			if f[0] != got {
				return fmt.Errorf("checksum de %s no coincide: esperado %s, obtenido %s", name, f[0], got)
			}
			return nil
		}
	}
	return fmt.Errorf("%s no aparece en checksums.txt", name)
}

func extractBinary(tgz []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s no está en el archivo", name)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}

// replaceExecutable swaps the running binary atomically (rename within the
// same directory); the running process keeps its old inode.
func replaceExecutable(exe string, bin []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".forge-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), exe)
}
