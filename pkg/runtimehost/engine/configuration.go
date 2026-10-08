package engine

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	hostconfig "github.com/pruefwerk/spex/pkg/runtimehost/definition"
)

// Both input adapters use the same configuration policy. These commands are
// read-only; they do not grant admission or create an acceptance environment.
func (h *Host) configuration(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("config requires artifact, select, scope or setup")
	}
	flags := flag.NewFlagSet("config "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", ".", "runtime checkout")
	service := flags.String("service", "", "artifact service")
	format := flags.String("format", "json", "json or tsv for artifact data")
	event := flags.String("event", "", "CI event")
	message := flags.String("message", "", "commit message")
	kinds := flags.String("kinds", "", "selected test kinds")
	groups := flags.String("groups", "", "selected test groups")
	file := flags.String("file", "", "scope document")
	workspace := flags.String("workspace", "", "generated test workspace")
	if err := flags.Parse(args[1:]); err != nil {
		return errors.New("invalid configuration command flags")
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected configuration command arguments")
	}
	if *format != "json" && (*format != "tsv" || args[0] != "artifact") && (*format != "env" || args[0] != "setup") {
		return errors.New("invalid configuration output format")
	}
	var result any
	switch args[0] {
	case "artifact":
		manifest, err := h.resolver.ReadArtifacts(*root)
		if err != nil {
			return err
		}
		pin, err := manifest.Select(*service)
		if err != nil {
			return err
		}
		if *format == "tsv" {
			_, err := fmt.Fprintln(stdout, strings.Join(pin.Fields(), "\t"))
			return err
		}
		result = pin
	case "select":
		selected, err := hostconfig.Select(*event, *message, *kinds, *groups)
		if err != nil {
			return err
		}
		result = selected
	case "scope":
		scope, err := h.resolver.LoadScope(*file)
		if err != nil {
			return err
		}
		name, _ := scope.Name()
		labels, _ := scope.Labels()
		result = struct {
			Scope  hostconfig.Scope  `json:"scope"`
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		}{scope, name, labels}
	case "setup":
		if *workspace == "" {
			return errors.New("generated workspace required")
		}
		setup, err := h.resolver.ReadSetup(filepath.Clean(*workspace))
		if err != nil {
			return err
		}
		result = setup.Environment()
		if *format == "env" {
			values := setup.Environment()
			keys := make([]string, 0, len(values))
			for key := range values {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if _, err := fmt.Fprintf(stdout, "%s=%s\n", key, values[key]); err != nil {
					return err
				}
			}
			return nil
		}
	default:
		return errors.New("unknown configuration command")
	}
	return json.NewEncoder(stdout).Encode(result)
}
