package runtimehelpers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

type Command func(context.Context, []string, []byte) ([]byte, error)
type HelmPolicy struct{ Namespace, ReceiptPrefix, Manager, Timeout string }

// Canonical hashing retains the previous receipt encoding: sorted JSON keys,
// compact separators and ASCII escapes. Receipts contain hashes, never values.
func digest(value any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return "", err
	}
	var normalized any
	if err := decodeJSON(buf.Bytes(), &normalized); err != nil {
		return "", err
	}
	buf.Reset()
	if err := enc.Encode(normalized); err != nil {
		return "", err
	}
	var out strings.Builder
	for _, r := range strings.TrimSuffix(buf.String(), "\n") {
		if r < 128 {
			out.WriteRune(r)
		} else {
			for _, u := range utf16.Encode([]rune{r}) {
				fmt.Fprintf(&out, "\\u%04x", u)
			}
		}
	}
	hash := sha256.Sum256([]byte(out.String()))
	return hex.EncodeToString(hash[:]), nil
}

func RequestFingerprint(chart string, arguments []string) (string, error) {
	files := map[string]string{}
	hashFile := func(path, key string) error {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("chart or values input is not a regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return errors.New("chart or values input unavailable")
		}
		sum := sha256.Sum256(data)
		files[key] = hex.EncodeToString(sum[:])
		return nil
	}
	for i, arg := range arguments {
		value := ""
		if arg == "--values" || arg == "-f" {
			if i+1 == len(arguments) {
				return "", errors.New("values file argument missing")
			}
			value = arguments[i+1]
		} else if strings.HasPrefix(arg, "--values=") || strings.HasPrefix(arg, "-f=") {
			_, value, _ = strings.Cut(arg, "=")
		}
		if value != "" {
			for _, path := range strings.Split(value, ",") {
				if err := hashFile(path, path); err != nil {
					return "", err
				}
			}
		}
	}
	if info, err := os.Stat(chart); err == nil && info.IsDir() {
		if err := hashFile(filepath.Join(chart, "Chart.yaml"), "chart/Chart.yaml"); err != nil {
			return "", err
		}
		if err := filepath.WalkDir(chart, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return errors.New("chart input unavailable")
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("local chart must not contain symlinks")
			}
			if entry.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(chart, path)
			if err != nil {
				return err
			}
			return hashFile(path, "chart/"+filepath.ToSlash(rel))
		}); err != nil {
			return "", err
		}
	}
	if arguments == nil {
		arguments = []string{}
	}
	return digest(struct {
		Chart     string            `json:"chart"`
		Arguments []string          `json:"arguments"`
		Files     map[string]string `json:"files"`
	}{chart, arguments, files})
}

func decodeJSON(data []byte, value any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if dec.Decode(value) != nil {
		return errors.New("invalid Helm response")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("invalid Helm response")
	}
	return nil
}

func EnsureHelm(ctx context.Context, kubeconfig, release, chart string, arguments []string, policy HelmPolicy, run Command, output io.Writer) error {
	name := regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	if !name.MatchString(policy.Namespace) || len(policy.Namespace) > 63 || !name.MatchString(release) || len(release) > 53 || !regexp.MustCompile(`^[a-z0-9-]{1,15}$`).MatchString(policy.ReceiptPrefix) || policy.Manager == "" || policy.Timeout == "" {
		return errors.New("invalid Helm reuse policy")
	}
	for _, arg := range arguments {
		key, _, _ := strings.Cut(arg, "=")
		switch key {
		case "--namespace", "-n", "--kubeconfig", "--kube-context", "--kube-apiserver", "--kube-token":
			return errors.New("Helm options must not override runtime target")
		}
	}
	requested, err := RequestFingerprint(chart, arguments)
	if err != nil {
		return err
	}
	if run == nil {
		run = RunTool
	}
	if output == nil {
		output = io.Discard
	}
	helm := func(verb string, extra ...string) ([]byte, error) {
		args := []string{"helm", verb, "--kubeconfig", kubeconfig, "--namespace", policy.Namespace}
		return run(ctx, append(args, extra...), nil)
	}
	kubectl := []string{"kubectl", "--kubeconfig", kubeconfig, "-n", policy.Namespace}
	data, err := helm("list", "--all", "--filter", "^"+release+"$", "--output", "json")
	if err != nil {
		return errors.New("Helm listing failed")
	}
	var rows []struct {
		Status string `json:"status"`
	}
	if decodeJSON(data, &rows) != nil || len(rows) > 1 {
		return errors.New("invalid Helm listing")
	}
	state := "absent"
	if len(rows) > 0 {
		state = rows[0].Status
	}
	if state != "absent" && state != "deployed" {
		return errors.New("shared release is not safely deployed")
	}
	hash, _ := digest(release)
	short := release
	if len(short) > 35 {
		short = short[:35]
	}
	receiptName := policy.ReceiptPrefix + "-" + short + "-" + hash[:10]
	recorded := map[string]string{}
	if state == "deployed" {
		data, err = run(ctx, append(append([]string{}, kubectl...), "get", "configmap", receiptName, "--ignore-not-found", "-o", "json"), nil)
		if err != nil {
			return errors.New("release receipt unavailable")
		}
		var receipt struct {
			Data map[string]string `json:"data"`
		}
		if decodeJSON(data, &receipt) != nil {
			return errors.New("release has no valid preparation receipt")
		}
		recorded = receipt.Data
		if recorded["requested"] != requested {
			return errors.New("requested chart or values changed; recreate disposable environment")
		}
	} else {
		args := []string{"helm", "upgrade", "--install", release, chart, "--kubeconfig", kubeconfig, "--namespace", policy.Namespace, "--create-namespace"}
		args = append(args, arguments...)
		args = append(args, "--wait", "--timeout", policy.Timeout)
		if _, err = run(ctx, args, nil); err != nil {
			return errors.New("Helm installation failed")
		}
	}
	status := func() (int, error) {
		data, err := helm("status", release, "--output", "json")
		if err != nil {
			return 0, errors.New("Helm status unavailable")
		}
		var s struct {
			Version int `json:"version"`
			Info    struct {
				Status string `json:"status"`
			} `json:"info"`
		}
		if decodeJSON(data, &s) != nil || s.Version < 1 || s.Info.Status != "deployed" {
			return 0, errors.New("release is no longer deployed with a valid revision")
		}
		return s.Version, nil
	}
	revision, err := status()
	if err != nil {
		return err
	}
	data, err = helm("get", "metadata", release, "--revision", strconv.Itoa(revision), "--output", "json")
	if err != nil {
		return errors.New("Helm metadata unavailable")
	}
	var metadata map[string]any
	if decodeJSON(data, &metadata) != nil {
		return errors.New("invalid Helm metadata")
	}
	chartName, ok := metadata["chart"].(string)
	version, vok := metadata["version"].(string)
	if !ok || !vok || chartName == "" || version == "" || metadata["name"] != release || metadata["namespace"] != policy.Namespace || metadata["status"] != "deployed" || metadata["revision"] != json.Number(strconv.Itoa(revision)) {
		return errors.New("inconsistent Helm metadata")
	}
	data, err = helm("get", "values", release, "--revision", strconv.Itoa(revision), "--output", "json")
	if err != nil {
		return errors.New("Helm values unavailable")
	}
	var values any
	if decodeJSON(data, &values) != nil {
		return errors.New("invalid Helm values")
	}
	current, err := status()
	if err != nil || current != revision {
		return errors.New("release changed while checking Helm inputs")
	}
	observed, err := digest(map[string]any{"revision": revision, "chart": metadata, "values": values})
	if err != nil {
		return errors.New("cannot fingerprint Helm inputs")
	}
	identity := chartName + "-" + version
	if state == "deployed" {
		if recorded["observed"] != observed {
			return errors.New("Helm configuration drifted; recreate disposable environment")
		}
		fmt.Fprintf(output, "Reusing checked shared Helm release %s (%s, revision %d)\n", release, identity, revision)
		return nil
	}
	receipt := struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name      string            `json:"name"`
			Namespace string            `json:"namespace"`
			Labels    map[string]string `json:"labels"`
		} `json:"metadata"`
		Data map[string]string `json:"data"`
	}{APIVersion: "v1", Kind: "ConfigMap"}
	receipt.Metadata.Name, receipt.Metadata.Namespace = receiptName, policy.Namespace
	receipt.Metadata.Labels = map[string]string{"app.kubernetes.io/managed-by": policy.Manager}
	receipt.Data = map[string]string{"requested": requested, "observed": observed, "chart": identity, "revision": strconv.Itoa(revision)}
	data, _ = json.Marshal(receipt)
	if _, err := run(ctx, append(append([]string{}, kubectl...), "apply", "-f", "-"), data); err != nil {
		return errors.New("cannot persist Helm preparation receipt")
	}
	fmt.Fprintf(output, "Installed and recorded shared Helm release %s (%s, revision %d)\n", release, identity, revision)
	return nil
}
